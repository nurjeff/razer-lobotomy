package main

var defaultBatteryProviders = []batteryProvider{
	newRazerBatteryProvider(),
}

type batteryProvider interface {
	Collect(devices []hidDeviceInfo) ([]batteryReading, []string)
}

func windowsBatteryProviders() []batteryProvider {
	return defaultBatteryProviders
}
