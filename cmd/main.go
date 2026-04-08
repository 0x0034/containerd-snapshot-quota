package main

import (
	"flag"
	"os"

	"k8s.io/klog/v2"

	"github.com/0x0034/containerd-snapshot-quota/pkg/config"
	"github.com/0x0034/containerd-snapshot-quota/pkg/quota"
)

func main() {
	configPath := flag.String("config", "/etc/quota-agent/config.yaml", "path to config file")
	klog.InitFlags(nil)
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		klog.Errorf("Failed to load config: %v", err)
		os.Exit(1)
	}

	agent, err := quota.New(cfg)
	if err != nil {
		klog.Errorf("Failed to create agent: %v", err)
		os.Exit(1)
	}

	if err := agent.Start(); err != nil {
		klog.Errorf("Failed to start agent: %v", err)
		os.Exit(1)
	}

	agent.WaitForShutdown()
}
