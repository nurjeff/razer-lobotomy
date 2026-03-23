package main

import (
	"errors"
	"fmt"
	"regexp"
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

	collectionSuffixRE = regexp.MustCompile(`(?i)&col[0-9a-f]{2}`)
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

func enumerateHIDDevices() ([]hidDeviceInfo, error) {
	hidGUID, err := getHIDGUID()
	if err != nil {
		return nil, err
	}

	infoSet, err := setupDiGetClassDevs(hidGUID)
	if err != nil {
		return nil, err
	}
	defer setupDiDestroyDeviceInfoList(infoSet)

	devices := make([]hidDeviceInfo, 0)
	for index := uint32(0); ; index++ {
		path, err := setupDiEnumDeviceInterfacePath(infoSet, hidGUID, index)
		if errors.Is(err, windows.ERROR_NO_MORE_ITEMS) {
			break
		}
		if err != nil {
			return nil, err
		}

		device, ok := inspectHIDDevice(path)
		if !ok {
			continue
		}
		devices = append(devices, device)
	}

	return devices, nil
}

func inspectHIDDevice(path string) (hidDeviceInfo, bool) {
	handle, err := openHIDDevice(path)
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

	manufacturer, _ := hidGetString(handle, procHidDGetManufacturerString)
	product, _ := hidGetString(handle, procHidDGetProductString)

	return hidDeviceInfo{
		Path:                    path,
		VendorID:                attributes.vendorID,
		ProductID:               attributes.productID,
		UsagePage:               caps.usagePage,
		Usage:                   caps.usage,
		FeatureReportByteLength: caps.featureReportByteLength,
		Manufacturer:            manufacturer,
		Product:                 product,
	}, true
}

func openHIDDevice(path string) (windows.Handle, error) {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}

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
			pathPtr,
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

func setupDiEnumDeviceInterfacePath(infoSet windows.Handle, classGUID *windows.GUID, index uint32) (string, error) {
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

	buffer := make([]byte, requiredSize)
	*(*uint32)(unsafe.Pointer(&buffer[0])) = detailDataCBSize()

	r1, _, err = procSetupDiGetDeviceInterfaceDetailW.Call(
		uintptr(infoSet),
		uintptr(unsafe.Pointer(&interfaceData)),
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(requiredSize),
		uintptr(unsafe.Pointer(&requiredSize)),
		0,
	)
	if r1 == 0 {
		return "", err
	}

	pathPtr := (*uint16)(unsafe.Pointer(uintptr(unsafe.Pointer(&buffer[0])) + unsafe.Sizeof(uint32(0))))
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

func hidGetString(handle windows.Handle, proc *windows.LazyProc) (string, error) {
	buf := make([]uint16, 126)
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
	if device.Product != "" {
		return device.Product
	}
	if device.Manufacturer != "" {
		return fmt.Sprintf("%s device", device.Manufacturer)
	}
	return device.Path
}

func normalizeDeviceKey(path string, vendorID uint16, productID uint16) string {
	normalized := strings.ToLower(path)
	normalized = collectionSuffixRE.ReplaceAllString(normalized, "")
	return fmt.Sprintf("%04x:%04x:%s", vendorID, productID, normalized)
}
