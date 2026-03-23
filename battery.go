package main

import (
	"sort"
	"strings"
	"time"
)

type batteryReading struct {
	Device   hidDeviceInfo
	Percent  int
	Raw      byte
	Charging *bool
	Backend  string
}

type batterySnapshot struct {
	Readings  []batteryReading
	Warnings  []string
	Err       error
	UpdatedAt time.Time
}

func run() error {
	return runTrayApp()
}

func collectBatterySnapshot() batterySnapshot {
	snapshot := batterySnapshot{UpdatedAt: time.Now()}

	devices, err := enumerateHIDDevices()
	if err != nil {
		snapshot.Err = err
		return snapshot
	}

	snapshot.Readings, snapshot.Warnings = collectBatteryReadings(devices, windowsBatteryProviders())
	if len(snapshot.Readings) == 0 && len(snapshot.Warnings) == 0 {
		snapshot.Warnings = append(snapshot.Warnings, "No supported battery-capable devices detected.")
	}

	return snapshot
}

func collectBatteryReadings(devices []hidDeviceInfo, providers []batteryProvider) ([]batteryReading, []string) {
	results := make([]batteryReading, 0)
	warnings := make([]string, 0)
	seen := make(map[string]struct{})

	for _, provider := range providers {
		providerReadings, providerWarnings := provider.Collect(devices)
		warnings = append(warnings, providerWarnings...)

		for _, reading := range providerReadings {
			key := normalizeDeviceKey(reading.Device.Path, reading.Device.VendorID, reading.Device.ProductID)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			results = append(results, reading)
		}
	}

	sort.Slice(results, func(i, j int) bool {
		left := strings.ToLower(deviceLabel(results[i].Device))
		right := strings.ToLower(deviceLabel(results[j].Device))
		if left == right {
			return normalizeDeviceKey(results[i].Device.Path, results[i].Device.VendorID, results[i].Device.ProductID) < normalizeDeviceKey(results[j].Device.Path, results[j].Device.VendorID, results[j].Device.ProductID)
		}
		return left < right
	})

	return results, warnings
}
