package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dustin/go-humanize"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/prom2json"
	"github.com/samuel/go-zookeeper/zk"
	"github.com/sirupsen/logrus"
)

var zkChroot string

func main() {
	var (
		flZkAddr           = flag.String("zk-addr", "", "The zookeeper host")
		flBrokerPromPort   = flag.String("broker-prom-port", "3451", "The broker prometheus exporter port to fetch partition metrics")
		flNodeExporterPort = flag.String("node-exporter-port", "19100", "The node_exporter port to fetch filesystem metrics")
		flDataMountpoint   = flag.String("data-mountpoint", "/data", "The mountpoint where data is stored to determine filesystem usage")
		flDryRun           = flag.Bool("dry-run", false, "Fetch the metrics but don't write them to ZooKeeper, instead print them")
	)

	flag.Parse()

	if *flZkAddr == "" {
		logrus.Fatal("please provide the zookeeper host with --zk-addr")
	}

	//

	var zkAddr string
	if pos := strings.IndexByte(*flZkAddr, '/'); pos >= 0 {
		zkAddr = (*flZkAddr)[:pos]
		zkChroot = (*flZkAddr)[pos:]
	} else {
		zkAddr = *flZkAddr
	}

	//

	zk.DefaultLogger = log.New(logrus.StandardLogger().Writer(), "", 0)

	zkConn, _, err := zk.Connect([]string{zkAddr}, 20*time.Second)
	if err != nil {
		logrus.Fatal(err)
	}
	defer zkConn.Close()

	// Get all broker ids

	const brokersPath = "/brokers/ids"

	zkPath := brokersPath
	if zkChroot != "" {
		zkPath = zkChroot + brokersPath
	}

	ids, _, err := zkConn.Children(zkPath)
	if err != nil {
		logrus.Fatal(err)
	}

	// Get all broker info

	type brokerID string

	prometheusHosts := make(map[brokerID]string)

	for _, id := range ids {
		data, _, err := zkConn.Get(zkPath + "/" + id)
		if err != nil {
			logrus.Fatal(err)
		}

		var obj struct {
			Host string `json:"host"`
		}

		if err := json.Unmarshal(data, &obj); err != nil {
			logrus.Fatal(err)
		}

		prometheusHosts[brokerID(id)] = obj.Host
	}

	//

	type sizeObject struct {
		Size float64
	}
	type storageFreeObject struct {
		StorageFree float64
	}
	type partitionSizeMap map[string]sizeObject
	type partitionMeta map[string]partitionSizeMap

	// Final mapping that will be written to Zookeeper for topicmappr
	partitionMapping := make(partitionMeta)

	brokerMetrics := make(map[brokerID]storageFreeObject)

	// Helper function that reads Prometheus metrics from a channel
	// and writes them to the global mapping

	for brokerID, host := range prometheusHosts {
		bid := brokerID

		logrus.Printf("fetching metrics for broker id %s at %s", bid, host)

		start := time.Now()

		// Get partition metrics from the broker
		fetchMetrics(host+":"+*flBrokerPromPort, func(wg *sync.WaitGroup, ch <-chan *dto.MetricFamily) {
			for m := range ch {
				mf := prom2json.NewFamily(m)

				if mf.Name != "kafka_log_size" {
					continue
				}

				// Expects a metric like this in the output.
				// kafka_log_size{partition="5",topic="foobar",} 0.0

				for _, m := range mf.Metrics {
					gauge := m.(prom2json.Metric)
					topic := gauge.Labels["topic"]
					partition := gauge.Labels["partition"]

					v, ok := partitionMapping[topic]
					if !ok {
						v = make(partitionSizeMap)
					}

					value, err := strconv.ParseFloat(gauge.Value, 10)
					if err != nil {
						logrus.Fatal(err)
					}

					// Keep the maximum observed size
					if value <= v[partition].Size {
						continue
					}

					v[partition] = sizeObject{Size: value}

					partitionMapping[topic] = v
				}
			}

			wg.Done()
		})

		// Get filesystem metrics from node_exporter
		fetchMetrics(host+":"+*flNodeExporterPort, func(wg *sync.WaitGroup, ch <-chan *dto.MetricFamily) {
			for m := range ch {
				mf := prom2json.NewFamily(m)

				if mf.Name != "node_filesystem_avail" {
					continue
				}

				// Expects a metric like this in the output.
				// node_filesystem_avail{device="/dev/md4",fstype="ext4",mountpoint="/data"} 7.245127430144e+12

				for _, m := range mf.Metrics {
					gauge := m.(prom2json.Metric)

					mountpoint, ok := gauge.Labels["mountpoint"]
					if !ok || mountpoint != *flDataMountpoint {
						continue
					}

					value, err := strconv.ParseFloat(gauge.Value, 10)
					if err != nil {
						logrus.Fatal(err)
					}

					brokerMetrics[bid] = storageFreeObject{StorageFree: value}
				}
			}

			wg.Done()
		})

		logrus.Printf("fetched metrics in %s", time.Since(start))
	}

	// Write to Zookeeper

	partitionMetaData, err := json.Marshal(partitionMapping)
	if err != nil {
		logrus.Fatal(err)
	}

	brokerMetricsData, err := json.Marshal(brokerMetrics)
	if err != nil {
		logrus.Fatal(err)
	}

	switch {
	case *flDryRun:
		// In dry-run don't do anything but display the information we retrieved and computed.

		logrus.Println("partition mapping")
		for topic, m := range partitionMapping {
			logrus.Printf("topic: %s", topic)

			type el struct {
				Partition int
				Size      uint64
			}

			var entries []el

			for partition, obj := range m {
				p, _ := strconv.Atoi(partition)

				entries = append(entries, el{
					Partition: p,
					Size:      uint64(obj.Size),
				})
			}

			sort.Slice(entries, func(i, j int) bool {
				return entries[i].Partition < entries[j].Partition
			})

			for _, entry := range entries {
				logrus.Printf("\tpartition %-4d size: %s", entry.Partition, humanize.Bytes(entry.Size))
			}
		}

		logrus.Println("fetched metrics")
		for brokerID, obj := range brokerMetrics {
			logrus.Printf("\tbroker #%-4s %15s: %s", brokerID, "storage free", humanize.Bytes(uint64(obj.StorageFree)))
		}

	default:
		if err := writeToZookeeper(zkConn, "partitionmeta", partitionMetaData); err != nil {
			logrus.Fatal(err)
		}
		if err := writeToZookeeper(zkConn, "brokermetrics", brokerMetricsData); err != nil {
			logrus.Fatal(err)
		}
	}
}

func writeToZookeeper(zkConn *zk.Conn, path string, data []byte) error {
	const root = "/topicmappr"

	// If our cluster is a zk chroot we need to use it too.

	var dir string
	if zkChroot != "" {
		dir = zkChroot + root
	} else {
		dir = root
	}
	path = dir + "/" + path

	// Remove the old node.
	err := zkConn.Delete(path, 0)
	if err != nil && err != zk.ErrNoNode {
		return fmt.Errorf("unable to delete path %s. err: %v", path, err)
	}

	// Create the directory node
	_, err = zkConn.Create(dir, nil, 0, zk.WorldACL(zk.PermAll))
	if err != nil && err != zk.ErrNodeExists {
		return fmt.Errorf("unable to create node %s. err: %v", path, err)
	}

	// Create the data node
	logrus.Printf("writing data to %s", path)

	_, err = zkConn.Create(path, data, 0, zk.WorldACL(zk.PermAll))
	if err != nil {
		return fmt.Errorf("unable to create path %s. err: %v", path, err)
	}
	return nil
}

func fetchMetrics(host string, fn func(wg *sync.WaitGroup, ch <-chan *dto.MetricFamily)) {
	resp, err := http.Get("http://" + host + "/metrics")
	if err != nil {
		logrus.Fatal(err)
	}
	defer resp.Body.Close()

	var (
		ch = make(chan *dto.MetricFamily)
		wg sync.WaitGroup
	)
	wg.Add(1)
	go fn(&wg, ch)

	err = prom2json.ParseResponse(resp, ch)
	if err != nil {
		logrus.Fatal(err)
	}

	wg.Wait()
}
