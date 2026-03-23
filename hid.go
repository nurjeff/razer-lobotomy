package main

import (
	"errors"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	digcfPresent         = 0x00000002
	digcfDeviceInterface = 0x00000010
	hidpStatusSuccess    = 0x00110000
)

var (
	modSetupAPI = windows.NewLazySystemDLL("setupapi.dll")
	modHID      = windows.NewLazySystemDLL("hid.dll")

	procSetupDiGetClassDevsW             = modSetupAPI.NewProc("SetupDiGetClassDevsW")
	procSetupDiEnumDeviceInterfaces      = modSetupAPI.NewProc("SetupDiEnumDeviceInterfaces")
	procSetupDiGetDeviceInterfaceDetailW = modSetupAPI.NewProc("SetupDiGetDeviceInterfaceDetailW")
	procSetupDiDestroyDeviceInfoList     = modSetupAPI.NewProc("SetupDiDestroyDeviceInfoList")

	procHidDGetHidGuid            = modHID.NewProc("HidD_GetHidGuid")
	procHidDGetAttributes         = modHID.NewProc("HidD_GetAttributes")
	procHidDGetManufacturerString = modHID.NewProc("HidD_GetManufacturerString")
	procHidDGetProductString      = modHID.NewProc("HidD_GetProductString")
	procHidDGetPreparsedData      = modHID.NewProc("HidD_GetPreparsedData")
	procHidDFreePreparsedData     = modHID.NewProc("HidD_FreePreparsedData")
	procHidDGetFeature            = modHID.NewProc("HidD_GetFeature")
	procHidDSetFeature            = modHID.NewProc("HidD_SetFeature")
	procHidPGetCaps               = modHID.NewProc("HidP_GetCaps")
)

type hidDeviceInfo struct {
	Path                    string
	VendorID                uint16
	ProductID               uint16
	UsagePage               uint16
	Usage                   uint16
	FeatureReportByteLength uint16
	Manufacturer            string
	Product                 string
	DisplayLabel            string
	SortKey                 string
	NormalizedKey           string
	pathUTF16               []uint16
}

type hidEnumerator struct {
	deviceCache      map[string]hidDeviceInfo
	deviceGeneration map[string]uint64
	generation       uint64
	detailBuffer     []byte
	stringBuffer     []uint16
	devices          []hidDeviceInfo
}

type spDeviceInterfaceData struct {
	cbSize             uint32
	interfaceClassGuid windows.GUID
	flags              uint32
	reserved           uintptr
}

type hiddAttributes struct {
	size          uint32
	vendorID      uint16
	productID     uint16
	versionNumber uint16
}

type hidpCaps struct {
	usage                     uint16
	usagePage                 uint16
	inputReportByteLength     uint16
	outputReportByteLength    uint16
	featureReportByteLength   uint16
	reserved                  [17]uint16
	numberLinkCollectionNodes uint16
	numberInputButtonCaps     uint16
	numberInputValueCaps      uint16
	numberInputDataIndices    uint16
	numberOutputButtonCaps    uint16
	numberOutputValueCaps     uint16
	numberOutputDataIndices   uint16
	numberFeatureButtonCaps   uint16
	numberFeatureValueCaps    uint16
	numberFeatureDataIndices  uint16
}

func newHIDEnumerator() *hidEnumerator {
	return &hidEnumerator{
		deviceCache:      make(map[string]hidDeviceInfo),
		deviceGeneration: make(map[string]uint64),
		stringBuffer:     make([]uint16, 126),
	}
}

func enumerateHIDDevices() ([]hidDeviceInfo, error) {
	return newHIDEnumerator().Enumerate()
}

func (enumerator *hidEnumerator) Enumerate() ([]hidDeviceInfo, error) {
	hidGUID, err := getHIDGUID()
	if err != nil {
		return nil, err
	}

	infoSet, err := setupDiGetClassDevs(hidGUID)
	if err != nil {
		return nil, err
	}
	defer setupDiDestroyDeviceInfoList(infoSet)

	enumerator.generation++
	enumerator.devices = enumerator.devices[:0]
	for index := uint32(0); ; index++ {
		path, err := enumerator.setupDiEnumDeviceInterfacePath(infoSet, hidGUID, index)
		if errors.Is(err, windows.ERROR_NO_MORE_ITEMS) {
			break
		}
		if err != nil {
			return nil, err
		}

		if device, ok := enumerator.deviceCache[path]; ok {
			enumerator.deviceGeneration[path] = enumerator.generation
			enumerator.devices = append(enumerator.devices, device)
			continue
		}

		pathUTF16, err := windows.UTF16FromString(path)
		if err != nil {
			continue
		}

		device, ok := inspectHIDDevice(path, pathUTF16, enumerator.stringBuffer)
		if !ok {
			continue
		}
		enumerator.deviceCache[path] = device
		enumerator.deviceGeneration[path] = enumerator.generation
		enumerator.devices = append(enumerator.devices, device)
	}
	enumerator.evictStaleDevices()

	return enumerator.devices, nil
}

func (enumerator *hidEnumerator) evictStaleDevices() {
	for path, generation := range enumerator.deviceGeneration {
		if generation == enumerator.generation {
			continue
		}
		delete(enumerator.deviceGeneration, path)
		delete(enumerator.deviceCache, path)
	}
}

func inspectHIDDevice(path string, pathUTF16 []uint16, stringBuffer []uint16) (hidDeviceInfo, bool) {
	handle, err := openHIDDeviceUTF16(pathUTF16)
	if err != nil {
		return hidDeviceInfo{}, false
	}
	defer windows.CloseHandle(handle)

	attributes, err := hidGetAttributes(handle)
	if err != nil {
		return hidDeviceInfo{}, false
	}

	caps, err := hidGetCaps(handle)
	if err != nil {
		return hidDeviceInfo{}, false
	}

	manufacturer, _ := hidGetString(handle, procHidDGetManufacturerString, stringBuffer)
	product, _ := hidGetString(handle, procHidDGetProductString, stringBuffer)
	label := path
	if product != "" {
		label = product
	} else if manufacturer != "" {
		label = manufacturer + " device"
	}

	return hidDeviceInfo{
		Path:                    path,
		VendorID:                attributes.vendorID,
		ProductID:               attributes.productID,
		UsagePage:               caps.usagePage,
		Usage:                   caps.usage,
		FeatureReportByteLength: caps.featureReportByteLength,
		Manufacturer:            manufacturer,
		Product:                 product,
		DisplayLabel:            label,
		SortKey:                 strings.ToLower(label),
		NormalizedKey:           normalizeDeviceKey(path, attributes.vendorID, attributes.productID),
		pathUTF16:               pathUTF16,
	}, true
}

func openHIDDevice(path string) (windows.Handle, error) {
	pathUTF16, err := windows.UTF16FromString(path)
	if err != nil {
		return 0, err
	}
	return openHIDDeviceUTF16(pathUTF16)
}

func openHIDDeviceInfo(device hidDeviceInfo) (windows.Handle, error) {
	if len(device.pathUTF16) > 0 {
		return openHIDDeviceUTF16(device.pathUTF16)
	}
	return openHIDDevice(device.Path)
}

func openHIDDeviceUTF16(pathUTF16 []uint16) (windows.Handle, error) {
	pathPtr := &pathUTF16[0]

	handle, err := windows.CreateFile(
		pathPtr,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		0,
		0,
	)
	if err != nil {
		handle, fallbackErr := windows.CreateFile(
			&pathUTF16[0],
			0,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
			nil,
			windows.OPEN_EXISTING,
			0,
			0,
		)
		if fallbackErr != nil {
			return 0, err
		}
		return handle, nil
	}

	return handle, nil
}

func getHIDGUID() (*windows.GUID, error) {
	var guid windows.GUID
	r1, _, err := procHidDGetHidGuid.Call(uintptr(unsafe.Pointer(&guid)))
	if r1 == 0 {
		return nil, err
	}
	return &guid, nil
}

func setupDiGetClassDevs(classGUID *windows.GUID) (windows.Handle, error) {
	r1, _, err := procSetupDiGetClassDevsW.Call(
		uintptr(unsafe.Pointer(classGUID)),
		0,
		0,
		uintptr(digcfPresent|digcfDeviceInterface),
	)
	handle := windows.Handle(r1)
	if handle == windows.InvalidHandle {
		return 0, err
	}
	return handle, nil
}

func (enumerator *hidEnumerator) setupDiEnumDeviceInterfacePath(infoSet windows.Handle, classGUID *windows.GUID, index uint32) (string, error) {
	interfaceData := spDeviceInterfaceData{cbSize: uint32(unsafe.Sizeof(spDeviceInterfaceData{}))}
	r1, _, err := procSetupDiEnumDeviceInterfaces.Call(
		uintptr(infoSet),
		0,
		uintptr(unsafe.Pointer(classGUID)),
		uintptr(index),
		uintptr(unsafe.Pointer(&interfaceData)),
	)
	if r1 == 0 {
		return "", err
	}

	var requiredSize uint32
	r1, _, err = procSetupDiGetDeviceInterfaceDetailW.Call(
		uintptr(infoSet),
		uintptr(unsafe.Pointer(&interfaceData)),
		0,
		0,
		uintptr(unsafe.Pointer(&requiredSize)),
		0,
	)
	if r1 == 0 && !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) {
		return "", err
	}

	if uint32(cap(enumerator.detailBuffer)) < requiredSize {
		enumerator.detailBuffer = make([]byte, requiredSize)
	}
	enumerator.detailBuffer = enumerator.detailBuffer[:requiredSize]
	*(*uint32)(unsafe.Pointer(&enumerator.detailBuffer[0])) = detailDataCBSize()

	r1, _, err = procSetupDiGetDeviceInterfaceDetailW.Call(
		uintptr(infoSet),
		uintptr(unsafe.Pointer(&interfaceData)),
		uintptr(unsafe.Pointer(&enumerator.detailBuffer[0])),
		uintptr(requiredSize),
		uintptr(unsafe.Pointer(&requiredSize)),
		0,
	)
	if r1 == 0 {
		return "", err
	}

	pathPtr := (*uint16)(unsafe.Pointer(uintptr(unsafe.Pointer(&enumerator.detailBuffer[0])) + unsafe.Sizeof(uint32(0))))
	return windows.UTF16PtrToString(pathPtr), nil
}

func setupDiDestroyDeviceInfoList(infoSet windows.Handle) {
	procSetupDiDestroyDeviceInfoList.Call(uintptr(infoSet))
}

func detailDataCBSize() uint32 {
	if unsafe.Sizeof(uintptr(0)) == 8 {
		return 8
	}
	return 6
}

func hidGetAttributes(handle windows.Handle) (hiddAttributes, error) {
	attributes := hiddAttributes{size: uint32(unsafe.Sizeof(hiddAttributes{}))}
	r1, _, err := procHidDGetAttributes.Call(uintptr(handle), uintptr(unsafe.Pointer(&attributes)))
	if r1 == 0 {
		return hiddAttributes{}, err
	}
	return attributes, nil
}

func hidGetCaps(handle windows.Handle) (hidpCaps, error) {
	var preparsed uintptr
	r1, _, err := procHidDGetPreparsedData.Call(uintptr(handle), uintptr(unsafe.Pointer(&preparsed)))
	if r1 == 0 {
		return hidpCaps{}, err
	}
	defer procHidDFreePreparsedData.Call(preparsed)

	var caps hidpCaps
	status, _, _ := procHidPGetCaps.Call(preparsed, uintptr(unsafe.Pointer(&caps)))
	if uint32(status) != hidpStatusSuccess {
		return hidpCaps{}, fmt.Errorf("HidP_GetCaps failed with status 0x%X", uint32(status))
	}

	return caps, nil
}

func hidGetString(handle windows.Handle, proc *windows.LazyProc, buf []uint16) (string, error) {
	buf[0] = 0
	r1, _, err := proc.Call(uintptr(handle), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)*2))
	if r1 == 0 {
		return "", err
	}
	return windows.UTF16ToString(buf), nil
}

func hidSetFeature(handle windows.Handle, report []byte) error {
	r1, _, err := procHidDSetFeature.Call(uintptr(handle), uintptr(unsafe.Pointer(&report[0])), uintptr(len(report)))
	if r1 == 0 {
		return err
	}
	return nil
}

func hidGetFeature(handle windows.Handle, report []byte) error {
	r1, _, err := procHidDGetFeature.Call(uintptr(handle), uintptr(unsafe.Pointer(&report[0])), uintptr(len(report)))
	if r1 == 0 {
		return err
	}
	return nil
}

func deviceLabel(device hidDeviceInfo) string {
	if device.DisplayLabel != "" {
		return device.DisplayLabel
	}
	if device.Product != "" {
		return device.Product
	}
	if device.Manufacturer != "" {
		return fmt.Sprintf("%s device", device.Manufacturer)
	}
	return device.Path
}

func deviceSortKey(device hidDeviceInfo) string {
	if device.SortKey != "" {
		return device.SortKey
	}
	return strings.ToLower(deviceLabel(device))
}

func deviceKey(device hidDeviceInfo) string {
	if device.NormalizedKey != "" {
		return device.NormalizedKey
	}
	return normalizeDeviceKey(device.Path, device.VendorID, device.ProductID)
}

func normalizeDeviceKey(path string, vendorID uint16, productID uint16) string {
	normalized := strings.ToLower(path)
	normalized = stripCollectionSuffix(normalized)
	return fmt.Sprintf("%04x:%04x:%s", vendorID, productID, normalized)
}

func stripCollectionSuffix(path string) string {
	length := len(path)
	if length < 6 {
		return path
	}
	index := length - 6
	if path[index] != '&' || path[index+1] != 'c' || path[index+2] != 'o' || path[index+3] != 'l' {
		return path
	}
	if !isHex(path[index+4]) || !isHex(path[index+5]) {
		return path
	}
	return path[:index]
}

func isHex(value byte) bool {
	return (value >= '0' && value <= '9') || (value >= 'a' && value <= 'f')
}
