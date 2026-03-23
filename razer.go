package main

import (
	"fmt"
	"time"

	"golang.org/x/sys/windows"
)

const (
	razerVendorID          = 0x1532
	razerReportLength      = 91
	razerCommandBattery    = 0x80
	razerCommandCharging   = 0x84
	razerCommandClassPower = 0x07
)

type razerBatteryProvider struct {
	profiles   []razerHardwareProfile
	results    []batteryReading
	candidates []hidDeviceInfo
	skipped    []hidDeviceInfo
	warnings   []string
	request    [razerReportLength]byte
	response   [razerReportLength]byte
}

type razerHardwareProfile struct {
	Name           string
	Match          func(device hidDeviceInfo) bool
	TransactionIDs []byte
	ReportIDs      []byte
}

type razerProfileRegistry struct {
	profiles []razerHardwareProfile
}

func newRazerBatteryProvider() batteryProvider {
	registry := &razerProfileRegistry{}
	registerBuiltinRazerProfiles(registry)
	return &razerBatteryProvider{profiles: registry.Profiles()}
}

func registerBuiltinRazerProfiles(registry *razerProfileRegistry) {
	registry.Register(razerHardwareProfile{
		Name:           "basilisk",
		Match:          matchRazerProductIDs(0x0271),
		TransactionIDs: []byte{0x9F, 0x3F, 0x1F},
		ReportIDs:      []byte{0x00, 0x01},
	})

	registry.Register(razerHardwareProfile{
		Name:           "generic-razer-battery",
		Match:          matchRazerVendor(),
		TransactionIDs: defaultRazerTransactionIDs(),
		ReportIDs:      defaultRazerReportIDs(),
	})
}

func (registry *razerProfileRegistry) Register(profile razerHardwareProfile) {
	if profile.Match == nil {
		profile.Match = matchRazerVendor()
	}
	if len(profile.TransactionIDs) == 0 {
		profile.TransactionIDs = defaultRazerTransactionIDs()
	}
	if len(profile.ReportIDs) == 0 {
		profile.ReportIDs = defaultRazerReportIDs()
	}
	profile.TransactionIDs = uniqueBytes(profile.TransactionIDs)
	profile.ReportIDs = uniqueBytes(profile.ReportIDs)
	registry.profiles = append(registry.profiles, profile)
}

func (registry *razerProfileRegistry) Profiles() []razerHardwareProfile {
	return registry.profiles
}

func (provider *razerBatteryProvider) Collect(devices []hidDeviceInfo) ([]batteryReading, []string) {
	provider.results = provider.results[:0]
	provider.candidates = provider.candidates[:0]
	provider.skipped = provider.skipped[:0]
	provider.warnings = provider.warnings[:0]

	for _, device := range devices {
		if device.VendorID != razerVendorID {
			continue
		}
		provider.candidates = append(provider.candidates, device)

		profile, ok := provider.profileFor(device)
		if !ok {
			provider.skipped = append(provider.skipped, device)
			continue
		}

		reading, ok := provider.tryRazerBattery(device, profile)
		if !ok {
			provider.skipped = append(provider.skipped, device)
			continue
		}

		provider.results = append(provider.results, reading)
	}

	provider.warnings = providerWarnings(provider.warnings[:0], provider.candidates, provider.skipped, len(provider.results) > 0)
	return provider.results, provider.warnings
}

func (provider *razerBatteryProvider) profileFor(device hidDeviceInfo) (razerHardwareProfile, bool) {
	for _, profile := range provider.profiles {
		if profile.Match(device) {
			return profile, true
		}
	}
	return razerHardwareProfile{}, false
}

func providerWarnings(warnings []string, candidates []hidDeviceInfo, skipped []hidDeviceInfo, haveResults bool) []string {
	if len(candidates) == 0 {
		return nil
	}

	warnings = warnings[:0]
	if !haveResults {
		warnings = append(warnings, "Found Razer HID interfaces, but none of them returned a battery report.")
		warnings = append(warnings, "That usually means the device needs a model-specific Razer profile.")
		for _, device := range candidates {
			warnings = append(warnings, fmt.Sprintf("- %s [%04X:%04X] usage=%04X/%04X featureReport=%d",
				deviceLabel(device), device.VendorID, device.ProductID, device.UsagePage, device.Usage, device.FeatureReportByteLength))
		}
		return warnings
	}

	if len(skipped) > 0 {
		warnings = append(warnings, fmt.Sprintf("Skipped %d Razer HID interface(s) that did not return a battery response.", len(skipped)))
	}
	return warnings
}

func (provider *razerBatteryProvider) tryRazerBattery(device hidDeviceInfo, profile razerHardwareProfile) (batteryReading, bool) {
	handle, err := openHIDDeviceInfo(device)
	if err != nil {
		return batteryReading{}, false
	}
	defer windows.CloseHandle(handle)

	for _, transactionID := range profile.TransactionIDs {
		for _, reportID := range profile.ReportIDs {
			rawBattery, ok := provider.queryRazerValue(handle, reportID, transactionID, razerCommandBattery)
			if !ok {
				continue
			}

			percent := scaleRazerBattery(rawBattery)
			chargingValue, chargingOK := provider.queryRazerValue(handle, reportID, transactionID, razerCommandCharging)

			reading := batteryReading{
				Device:      device,
				Percent:     percent,
				Raw:         rawBattery,
				HasCharging: chargingOK,
				Charging:    chargingValue != 0,
				Backend:     fmt.Sprintf("Razer profile %s report 0x%02X transaction 0x%02X", profile.Name, reportID, transactionID),
			}

			return reading, true
		}
	}

	return batteryReading{}, false
}

func (provider *razerBatteryProvider) queryRazerValue(handle windows.Handle, reportID byte, transactionID byte, commandID byte) (byte, bool) {
	request := provider.request[:]
	response := provider.response[:]
	clear(request)
	buildRazerRequest(request, reportID, transactionID, commandID)
	if err := hidSetFeature(handle, request); err != nil {
		return 0, false
	}

	time.Sleep(8 * time.Millisecond)

	response[0] = request[0]
	if err := hidGetFeature(handle, response); err != nil {
		return 0, false
	}

	if !isValidRazerResponse(request, response) {
		return 0, false
	}

	return response[10], true
}

func buildRazerRequest(report []byte, reportID byte, transactionID byte, commandID byte) {
	report[0] = reportID
	report[1] = 0x00
	report[2] = transactionID
	report[3] = 0x00
	report[4] = 0x00
	report[5] = 0x00
	report[6] = 0x02
	report[7] = razerCommandClassPower
	report[8] = commandID
	report[89] = razerCRC(report)
}

func isValidRazerResponse(request []byte, response []byte) bool {
	if len(response) != razerReportLength {
		return false
	}
	if response[1] != 0x02 {
		return false
	}
	if response[7] != request[7] || response[8] != request[8] {
		return false
	}
	if response[6] < 0x02 {
		return false
	}
	return true
}

func razerCRC(report []byte) byte {
	var crc byte
	for index := 3; index < 89; index++ {
		crc ^= report[index]
	}
	return crc
}

func scaleRazerBattery(raw byte) int {
	percent := (int(raw)*100 + 127) / 255
	if percent < 0 {
		return 0
	}
	if percent > 100 {
		return 100
	}
	return percent
}

func matchRazerVendor() func(device hidDeviceInfo) bool {
	return func(device hidDeviceInfo) bool {
		return device.VendorID == razerVendorID
	}
}

func matchRazerProductIDs(productIDs ...uint16) func(device hidDeviceInfo) bool {
	allowed := make(map[uint16]struct{}, len(productIDs))
	for _, productID := range productIDs {
		allowed[productID] = struct{}{}
	}

	return func(device hidDeviceInfo) bool {
		if device.VendorID != razerVendorID {
			return false
		}
		_, ok := allowed[device.ProductID]
		return ok
	}
}

func defaultRazerTransactionIDs() []byte {
	return []byte{0x1F, 0x3F, 0x9F, 0xFF}
}

func defaultRazerReportIDs() []byte {
	return []byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07}
}

func uniqueBytes(values []byte) []byte {
	result := make([]byte, 0, len(values))
	seen := make(map[byte]struct{}, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
