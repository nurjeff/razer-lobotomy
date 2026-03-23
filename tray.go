package main

import (
	"fmt"
	"strings"
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
	systray.SetIcon(buildTrayIcon(100, false, true))

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
	snapshot := collectBatterySnapshot()
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
	summary := app.summaryLine(snapshot)
	app.statusItem.SetTitle(summary)
	app.statusItem.SetTooltip(summary)
	app.updatedItem.SetTitle("Last updated: " + snapshot.UpdatedAt.Format("15:04:05"))
	app.updatedItem.SetTooltip(snapshot.UpdatedAt.Format(time.RFC3339))

	iconPercent, charging, problem := snapshotIconState(snapshot)
	systray.SetIcon(buildTrayIcon(iconPercent, charging, problem))
	systray.SetTooltip(buildTooltip(summary, snapshot))

	app.syncDeviceItems(snapshot)
	app.syncWarningItems(snapshot)
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
		if reading.Charging != nil && *reading.Charging {
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
	if reading.Charging != nil && *reading.Charging {
		line += " charging"
	}
	return line
}

func buildTooltip(summary string, snapshot batterySnapshot) string {
	lines := []string{summary}

	if snapshot.Err != nil {
		lines = append(lines, snapshot.Err.Error())
		return truncateTooltip(strings.Join(lines, "\n"))
	}

	for index, reading := range snapshot.Readings {
		if index == 3 {
			lines = append(lines, fmt.Sprintf("+%d more device(s)", len(snapshot.Readings)-index))
			break
		}
		lines = append(lines, formatReadingLine(reading))
	}

	if len(snapshot.Readings) == 0 && len(snapshot.Warnings) > 0 {
		lines = append(lines, snapshot.Warnings[0])
	}

	return truncateTooltip(strings.Join(lines, "\n"))
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

func snapshotIconState(snapshot batterySnapshot) (int, bool, bool) {
	if len(snapshot.Readings) == 0 {
		return 0, false, true
	}

	lowest := 100
	charging := false
	for _, reading := range snapshot.Readings {
		if reading.Percent < lowest {
			lowest = reading.Percent
		}
		if reading.Charging != nil && *reading.Charging {
			charging = true
		}
	}

	return lowest, charging, snapshot.Err != nil
}
