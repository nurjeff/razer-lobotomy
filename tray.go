package main

import (
	"fmt"
	"sync"
	"time"

	"github.com/getlantern/systray"
)

const (
	trayRefreshInterval = 5 * time.Second
	maxTooltipLength    = 120
)

type trayApp struct {
	refreshInterval time.Duration
	snapshotCh      chan batterySnapshot
	refreshCh       chan struct{}
	stopCh          chan struct{}
	stopOnce        sync.Once

	statusItem   *systray.MenuItem
	devicesMenu  *systray.MenuItem
	warningsMenu *systray.MenuItem
	updatedItem  *systray.MenuItem
	refreshItem  *systray.MenuItem
	quitItem     *systray.MenuItem

	deviceItems  []*systray.MenuItem
	warningItems []*systray.MenuItem
	lastSnapshot *batterySnapshot
	lastIcon     trayIconState
}

type trayIconState struct {
	percent  int
	charging bool
	alert    bool
}

func runTrayApp() error {
	app := &trayApp{
		refreshInterval: trayRefreshInterval,
		snapshotCh:      make(chan batterySnapshot, 1),
		refreshCh:       make(chan struct{}, 1),
		stopCh:          make(chan struct{}),
	}

	systray.Run(app.onReady, app.onExit)
	return nil
}

func (app *trayApp) onReady() {
	systray.SetTitle("Battery Driver")
	systray.SetTooltip("Starting battery monitor")
	app.lastIcon = trayIconState{percent: 100, alert: true}
	systray.SetIcon(buildTrayIcon(app.lastIcon.percent, app.lastIcon.charging, app.lastIcon.alert))

	app.statusItem = systray.AddMenuItem("Starting battery monitor...", "")
	app.statusItem.Disable()

	app.devicesMenu = systray.AddMenuItem("Devices", "Detected battery devices")
	app.warningsMenu = systray.AddMenuItem("Status", "Warnings and probe status")
	app.updatedItem = systray.AddMenuItem("Last updated: starting...", "")
	app.updatedItem.Disable()

	systray.AddSeparator()
	app.refreshItem = systray.AddMenuItem("Refresh now", "Refresh battery status immediately")
	app.quitItem = systray.AddMenuItem("Quit", "Exit battery monitor")

	go app.eventLoop()
	go app.collectLoop()
	app.requestRefresh()
}

func (app *trayApp) onExit() {
	app.stop()
}

func (app *trayApp) stop() {
	app.stopOnce.Do(func() {
		close(app.stopCh)
	})
}

func (app *trayApp) requestRefresh() {
	select {
	case app.refreshCh <- struct{}{}:
	default:
	}
}

func (app *trayApp) collectLoop() {
	ticker := time.NewTicker(app.refreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-app.stopCh:
			return
		case <-ticker.C:
			app.publishSnapshot()
		case <-app.refreshCh:
			app.publishSnapshot()
		}
	}
}

func (app *trayApp) publishSnapshot() {
	snapshot, changed := defaultBatteryMonitor.collectSnapshotIfChanged()
	if !changed {
		return
	}
	select {
	case app.snapshotCh <- snapshot:
	case <-app.stopCh:
	}
}

func (app *trayApp) eventLoop() {
	for {
		select {
		case <-app.stopCh:
			return
		case <-app.refreshItem.ClickedCh:
			app.requestRefresh()
		case <-app.quitItem.ClickedCh:
			app.stop()
			systray.Quit()
			return
		case snapshot := <-app.snapshotCh:
			app.applySnapshot(snapshot)
		}
	}
}

func (app *trayApp) applySnapshot(snapshot batterySnapshot) {
	if app.lastSnapshot != nil && snapshotsEqual(*app.lastSnapshot, snapshot) {
		return
	}

	summary := app.summaryLine(snapshot)
	app.statusItem.SetTitle(summary)
	app.statusItem.SetTooltip(summary)
	app.updatedItem.SetTitle("Last updated: " + snapshot.UpdatedAt.Format("15:04:05"))
	app.updatedItem.SetTooltip(snapshot.UpdatedAt.Format(time.RFC3339))

	iconState := snapshotIconState(snapshot)
	if iconState != app.lastIcon {
		app.lastIcon = iconState
		systray.SetIcon(buildTrayIcon(iconState.percent, iconState.charging, iconState.alert))
	}
	systray.SetTooltip(buildTooltip(summary, snapshot))

	app.syncDeviceItems(snapshot)
	app.syncWarningItems(snapshot)
	app.lastSnapshot = cloneSnapshot(snapshot)
}

func (app *trayApp) summaryLine(snapshot batterySnapshot) string {
	if snapshot.Err != nil {
		return "Battery status unavailable"
	}

	count := len(snapshot.Readings)
	if count == 0 {
		return "No supported battery devices detected"
	}

	chargingCount := 0
	for _, reading := range snapshot.Readings {
		if reading.HasCharging && reading.Charging {
			chargingCount++
		}
	}

	if chargingCount == 0 {
		return fmt.Sprintf("%d device(s) detected", count)
	}

	return fmt.Sprintf("%d device(s) detected, %d charging", count, chargingCount)
}

func (app *trayApp) syncDeviceItems(snapshot batterySnapshot) {
	lines := make([]string, 0, len(snapshot.Readings))
	for _, reading := range snapshot.Readings {
		lines = append(lines, formatReadingLine(reading))
	}
	if len(lines) == 0 {
		lines = append(lines, "No supported battery-capable devices detected.")
	}

	app.devicesMenu.SetTitle(fmt.Sprintf("Devices (%d)", len(snapshot.Readings)))
	app.devicesMenu.SetTooltip("Detected battery-backed devices")
	app.deviceItems = ensureSubmenuItems(app.devicesMenu, app.deviceItems, len(lines))
	for index, line := range lines {
		item := app.deviceItems[index]
		item.SetTitle(line)
		item.SetTooltip(line)
		item.Disable()
		item.Show()
	}
	for _, item := range app.deviceItems[len(lines):] {
		item.Hide()
	}
}

func (app *trayApp) syncWarningItems(snapshot batterySnapshot) {
	lines := make([]string, 0, len(snapshot.Warnings)+1)
	if snapshot.Err != nil {
		lines = append(lines, snapshot.Err.Error())
	}
	lines = append(lines, snapshot.Warnings...)

	if len(lines) == 0 {
		app.warningsMenu.Hide()
		for _, item := range app.warningItems {
			item.Hide()
		}
		return
	}

	app.warningsMenu.Show()
	app.warningsMenu.SetTitle(fmt.Sprintf("Status (%d)", len(lines)))
	app.warningsMenu.SetTooltip("Warnings and device probe status")
	app.warningItems = ensureSubmenuItems(app.warningsMenu, app.warningItems, len(lines))
	for index, line := range lines {
		item := app.warningItems[index]
		item.SetTitle(line)
		item.SetTooltip(line)
		item.Disable()
		item.Show()
	}
	for _, item := range app.warningItems[len(lines):] {
		item.Hide()
	}
}

func ensureSubmenuItems(parent *systray.MenuItem, items []*systray.MenuItem, count int) []*systray.MenuItem {
	for len(items) < count {
		item := parent.AddSubMenuItem("", "")
		item.Disable()
		items = append(items, item)
	}
	return items
}

func formatReadingLine(reading batteryReading) string {
	line := fmt.Sprintf("%s: %d%%", deviceLabel(reading.Device), reading.Percent)
	if reading.HasCharging && reading.Charging {
		line += " charging"
	}
	return line
}

func buildTooltip(summary string, snapshot batterySnapshot) string {
	buf := make([]byte, 0, maxTooltipLength)
	buf = append(buf, summary...)

	if snapshot.Err != nil {
		buf = appendTooltipLine(buf, snapshot.Err.Error())
		return truncateTooltip(string(buf))
	}

	for index, reading := range snapshot.Readings {
		if index == 3 {
			buf = appendTooltipLine(buf, fmt.Sprintf("+%d more device(s)", len(snapshot.Readings)-index))
			break
		}
		buf = appendTooltipLine(buf, formatReadingLine(reading))
	}

	if len(snapshot.Readings) == 0 && len(snapshot.Warnings) > 0 {
		buf = appendTooltipLine(buf, snapshot.Warnings[0])
	}

	return truncateTooltip(string(buf))
}

func appendTooltipLine(buf []byte, line string) []byte {
	buf = append(buf, '\n')
	buf = append(buf, line...)
	return buf
}

func truncateTooltip(value string) string {
	if len(value) <= maxTooltipLength {
		return value
	}
	if maxTooltipLength <= 3 {
		return value[:maxTooltipLength]
	}
	return value[:maxTooltipLength-3] + "..."
}

func snapshotIconState(snapshot batterySnapshot) trayIconState {
	if len(snapshot.Readings) == 0 {
		return trayIconState{percent: 0, charging: false, alert: true}
	}

	lowest := 100
	charging := false
	for _, reading := range snapshot.Readings {
		if reading.Percent < lowest {
			lowest = reading.Percent
		}
		if reading.HasCharging && reading.Charging {
			charging = true
		}
	}

	return trayIconState{percent: lowest, charging: charging, alert: snapshot.Err != nil}
}

func snapshotsEqual(left batterySnapshot, right batterySnapshot) bool {
	if (left.Err == nil) != (right.Err == nil) {
		return false
	}
	if left.Err != nil && left.Err.Error() != right.Err.Error() {
		return false
	}
	if len(left.Warnings) != len(right.Warnings) || len(left.Readings) != len(right.Readings) {
		return false
	}
	for index := range left.Warnings {
		if left.Warnings[index] != right.Warnings[index] {
			return false
		}
	}
	for index := range left.Readings {
		if !readingsEqual(left.Readings[index], right.Readings[index]) {
			return false
		}
	}
	return true
}

func readingsEqual(left batteryReading, right batteryReading) bool {
	if left.Device.Path != right.Device.Path || left.Device.VendorID != right.Device.VendorID || left.Device.ProductID != right.Device.ProductID {
		return false
	}
	if left.Percent != right.Percent || left.Raw != right.Raw || left.Backend != right.Backend {
		return false
	}
	if left.HasCharging != right.HasCharging {
		return false
	}
	if left.Charging != right.Charging {
		return false
	}
	return true
}

func cloneSnapshot(snapshot batterySnapshot) *batterySnapshot {
	clone := batterySnapshot{
		Err:       snapshot.Err,
		UpdatedAt: snapshot.UpdatedAt,
	}
	if len(snapshot.Readings) > 0 {
		clone.Readings = make([]batteryReading, len(snapshot.Readings))
		for index, reading := range snapshot.Readings {
			clone.Readings[index] = cloneReading(reading)
		}
	}
	if len(snapshot.Warnings) > 0 {
		clone.Warnings = make([]string, len(snapshot.Warnings))
		copy(clone.Warnings, snapshot.Warnings)
	}
	return &clone
}

func cloneReading(reading batteryReading) batteryReading {
	return reading
}
