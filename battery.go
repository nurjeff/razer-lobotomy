package main

import (
	"sort"
	"time"
)

var defaultBatteryMonitor = newBatteryMonitor()

type batteryReading struct {
	Device      hidDeviceInfo
	Percent     int
	Raw         byte
	Charging    bool
	HasCharging bool
	Backend     string
}

type batterySnapshot struct {
	Readings  []batteryReading
	Warnings  []string
	Err       error
	UpdatedAt time.Time
}

type batteryMonitor struct {
	enumerator   *hidEnumerator
	providers    []batteryProvider
	results      []batteryReading
	warnings     []string
	seen         map[string]struct{}
	lastSnapshot batterySnapshot
	hasSnapshot  bool
}

func run() error {
	return runTrayApp()
}

func newBatteryMonitor() *batteryMonitor {
	return &batteryMonitor{
		enumerator: newHIDEnumerator(),
		providers:  windowsBatteryProviders(),
		seen:       make(map[string]struct{}),
	}
}

func collectBatterySnapshot() batterySnapshot {
	snapshot, _ := defaultBatteryMonitor.collectSnapshotIfChanged()
	return snapshot
}

func collectBatteryReadings(devices []hidDeviceInfo, providers []batteryProvider) ([]batteryReading, []string) {
	monitor := batteryMonitor{providers: providers, seen: make(map[string]struct{})}
	monitor.collectReadings(devices)
	return monitor.results, monitor.warnings
}

func (monitor *batteryMonitor) collectSnapshot() batterySnapshot {
	snapshot, _ := monitor.collectSnapshotIfChanged()
	return snapshot
}

func (monitor *batteryMonitor) collectSnapshotIfChanged() (batterySnapshot, bool) {
	snapshot := batterySnapshot{UpdatedAt: time.Now()}

	devices, err := monitor.enumerator.Enumerate()
	if err != nil {
		snapshot.Err = err
		if monitor.hasSnapshot && snapshotsEqual(monitor.lastSnapshot, snapshot) {
			return batterySnapshot{}, false
		}
		monitor.lastSnapshot = snapshot
		monitor.hasSnapshot = true
		return snapshot, true
	}

	monitor.collectReadings(devices)
	if len(monitor.results) == 0 && len(monitor.warnings) == 0 {
		monitor.warnings = append(monitor.warnings, "No supported battery-capable devices detected.")
	}

	snapshot.Readings = monitor.results
	snapshot.Warnings = monitor.warnings
	if monitor.hasSnapshot && snapshotsEqual(monitor.lastSnapshot, snapshot) {
		return batterySnapshot{}, false
	}

	if len(monitor.results) > 0 {
		snapshot.Readings = make([]batteryReading, len(monitor.results))
		copy(snapshot.Readings, monitor.results)
	}
	if len(monitor.warnings) > 0 {
		snapshot.Warnings = make([]string, len(monitor.warnings))
		copy(snapshot.Warnings, monitor.warnings)
	}
	monitor.lastSnapshot = snapshot
	monitor.hasSnapshot = true

	return snapshot, true
}

func (monitor *batteryMonitor) collectReadings(devices []hidDeviceInfo) {
	monitor.results = monitor.results[:0]
	monitor.warnings = monitor.warnings[:0]
	clearSeenSet(monitor.seen)

	for _, provider := range monitor.providers {
		providerReadings, providerWarnings := provider.Collect(devices)
		monitor.warnings = append(monitor.warnings, providerWarnings...)

		for _, reading := range providerReadings {
			key := deviceKey(reading.Device)
			if _, exists := monitor.seen[key]; exists {
				continue
			}
			monitor.seen[key] = struct{}{}
			monitor.results = append(monitor.results, reading)
		}
	}

	sort.Slice(monitor.results, func(i, j int) bool {
		left := deviceSortKey(monitor.results[i].Device)
		right := deviceSortKey(monitor.results[j].Device)
		if left == right {
			return deviceKey(monitor.results[i].Device) < deviceKey(monitor.results[j].Device)
		}
		return left < right
	})
}

func clearSeenSet(values map[string]struct{}) {
	for key := range values {
		delete(values, key)
	}
}
