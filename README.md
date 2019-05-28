# kafkakit-prometheus

This repository contains tools to make [kafka-kit](https://github.com/DataDog/kafka-kit) work with brokers that expose Prometheus metrics.

The original [metricsfetcher](https://github.com/DataDog/kafka-kit/tree/master/cmd/metricsfetcher) from _kafka-kit_ depends on DataDog,
however [topicmappr](https://github.com/DataDog/kafka-kit/tree/master/cmd/topicmappr) is provider-agnostic.

## metricsfetcher

Fetches partition and storage metrics from the brokers via a Prometheus endpoint.

[README](metricsfetcher/README.md)
