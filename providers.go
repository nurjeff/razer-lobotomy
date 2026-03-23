package main

type batteryProvider interface {
	Collect(devices []hidDeviceInfo) ([]batteryReading, []string)
}

func windowsBatteryProviders() []batteryProvider {
	return []batteryProvider{
		newRazerBatteryProvider(),
	}
}
