# metrivsfetcher

This tool fetches partition and storage metrics from brokers exposing a Prometheus endpoint and stores them using the original [format](https://github.com/DataDog/kafka-kit/tree/master/cmd/metricsfetcher#data-structures) in ZooKeeper.

## Requirements

* Go 1.12
* The prometheus\_jmx\_exporter configured for your Kafka brokers
* The node\_exporter configured on your Kafka brokers

You'll also need to configure your exporters so that they produce these metrics:

The log size gauge:

    kafka_log_size{partition="5",topic="foobar",} 0.0

The filesystem available gauge:

    node_filesystem_avail{device="/dev/md4",fstype="ext4",mountpoint="/data"} 7.245127430144e+12

## Installation

Because of a dependency which contains a `vendor` directory `go get` will not work.

You need to run this outside your GOPATH:

    git clone https://github.com/BatchLabs/kafkakit-prometheus
    go install ./...

## Usage

See the help of the command:

    Usage of metricsfetcher:
      -broker-prom-port string
            The broker prometheus exporter port to fetch partition metrics (default "3451")
      -data-mountpoint string
            The mountpoint where data is stored to determine filesystem usage (default "/data")
      -node-exporter-port string
            The node_exporter port to fetch filesystem metrics (default "19100")
      -zk-addr string
            The zookeeper host

Example:

    metricsfetcher --zk-addr zk1:2181 --broker-prom-port 3400 --node-exporter-port 18000 --data-mountpoint /

Note that if you have a lot of brokers it will take a while, especially since the `prometheus_jmx_exporter` is slow.
