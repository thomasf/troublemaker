package main

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

type EnvInfo struct {
	CgroupVersion string `json:"cgroup_version"`
	CgroupPath    string `json:"cgroup_path"`
	MemoryLimit   int64  `json:"memory_limit_bytes"`
	MemoryUsage   int64  `json:"memory_usage_bytes"`
	CPUQuota      string `json:"cpu_quota"` // v2 format: "quota period"
}

// readParam reads a single string value from a cgroup file
func readParam(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// parseInt64 converts cgroup strings to int64, handling "max" as -1
func parseInt64(val string) int64 {
	if val == "" || val == "max" {
		return -1
	}
	i, _ := strconv.ParseInt(val, 10, 64)
	return i
}

func GetEnvInfo() *EnvInfo {
	info := &EnvInfo{}

	// 1. Identify Cgroup Path and Version
	file, err := os.Open("/proc/self/cgroup")
	if err == nil {
		defer file.Close()
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			parts := strings.Split(scanner.Text(), ":")
			if len(parts) < 3 {
				continue
			}
			info.CgroupPath = parts[2]
			if parts[0] == "0" && parts[1] == "" {
				info.CgroupVersion = "v2"
			} else {
				info.CgroupVersion = "v1"
			}
		}
	}

	// 2. Resource Data (Focusing on Cgroup v2 paths)
	// Base path for v2 is usually /sys/fs/cgroup
	info.MemoryLimit = parseInt64(readParam("/sys/fs/cgroup/memory.max"))
	info.MemoryUsage = parseInt64(readParam("/sys/fs/cgroup/memory.current"))
	info.CPUQuota = readParam("/sys/fs/cgroup/cpu.max")

	// Fallback for v1 if v2 data is missing
	if info.CgroupVersion == "v1" {
		if info.MemoryLimit == -1 {
			info.MemoryLimit = parseInt64(readParam("/sys/fs/cgroup/memory/memory.limit_in_bytes"))
		}
		if info.MemoryUsage == -1 {
			info.MemoryUsage = parseInt64(readParam("/sys/fs/cgroup/memory/memory.usage_in_bytes"))
		}
	}

	return info
}
