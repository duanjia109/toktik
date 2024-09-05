package prom

import "github.com/prometheus/client_golang/prometheus"

// prometheus.Registry 是 Prometheus 监控系统中用于注册和管理指标的接口。
// 它提供了注册指标、注销指标等功能。
var Client = prometheus.NewRegistry()
