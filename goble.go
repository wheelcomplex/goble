package goble

/*
#include <xpc/xpc.h>
#include "xpc_wrapper.h"
*/
import "C"

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"log"
	"time"
)

//
// BLE support
//

var STATES = []string{"unknown", "resetting", "unsupported", "unauthorized", "poweredOff", "poweredOn"}

type Property int

const (
	Broadcast                 Property = 1 << iota
	Read                               = 1 << iota
	WriteWithoutResponse               = 1 << iota
	Write                              = 1 << iota
	Notify                             = 1 << iota
	Indicate                           = 1 << iota
	AuthenticatedSignedWrites          = 1 << iota
	ExtendedProperties                 = 1 << iota
)

func (p Property) Readable() bool {
	return (p & Read) != 0
}

func (p Property) String() (result string) {
	if (p & Broadcast) != 0 {
		result += "broadcast "
	}
	if (p & Read) != 0 {
		result += "read "
	}
	if (p & WriteWithoutResponse) != 0 {
		result += "writeWithoutResponse "
	}
	if (p & Write) != 0 {
		result += "write "
	}
	if (p & Notify) != 0 {
		result += "notify "
	}
	if (p & Indicate) != 0 {
		result += "indicate "
	}
	if (p & AuthenticatedSignedWrites) != 0 {
		result += "authenticateSignedWrites "
	}
	if (p & ExtendedProperties) != 0 {
		result += "extendedProperties "
	}

	return
}

type ServiceData struct {
	Uuid string
	Data []byte
}

type CharacteristicDescriptor struct {
	Uuid   string
	Handle int
}

type ServiceCharacteristic struct {
	Uuid        string
	Name        string
	Type        string
	Properties  Property
	Descriptors map[interface{}]*CharacteristicDescriptor
	Handle      int
	ValueHandle int
}

type ServiceHandle struct {
	Uuid            string
	Name            string
	Type            string
	Characteristics map[interface{}]*ServiceCharacteristic
	startHandle     int
	endHandle       int
}

type Advertisement struct {
	LocalName        string
	TxPowerLevel     int
	ManufacturerData []byte
	ServiceData      []ServiceData
	ServiceUuids     []string
}

type Peripheral struct {
	Uuid          UUID
	Advertisement Advertisement
	Rssi          int
	Services      map[interface{}]*ServiceHandle
}

// GATT Descriptor
type Descriptor struct {
	uuid  UUID
	value []byte
}

// GATT Characteristic
type Characteristic struct {
	uuid        UUID
	properties  Property
	secure      Property
	descriptors []Descriptor
	value       []byte
}

// GATT Service
type Service struct {
	uuid            UUID
	characteristics []Characteristic
}

type BLE struct {
	Emitter
	conn    C.xpc_connection_t
	verbose bool

	peripherals            map[string]*Peripheral
	attributes             array
	lastServiceAttributeId int
	allowDuplicates        bool
}

func New() *BLE {
	ble := &BLE{peripherals: map[string]*Peripheral{}, Emitter: Emitter{}}
	ble.Emitter.Init()
	ble.conn = XpcConnect("com.apple.blued", ble)
	return ble
}

func (ble *BLE) SetVerbose(v bool) {
	ble.verbose = v
}

// process BLE events and asynchronous errors
// (implements XpcEventHandler)
func (ble *BLE) HandleXpcEvent(event dict, err error) {
	if err != nil {
		log.Println("error:", err)
		if event == nil {
			return
		}
	}

	id := event.MustGetInt("kCBMsgId")
	args := event.MustGetDict("kCBMsgArgs")

	if ble.verbose {
		log.Printf("event: %v %#v\n", id, args)
	}

	switch id {
	case 6: // state change
		state := args.MustGetInt("kCBMsgArgState")
		ble.Emit(Event{Name: "stateChange", State: STATES[state]})

	case 16: // advertising start
		result := args.MustGetInt("kCBMsgArgResult")
		if result != 0 {
			log.Printf("event: error in advertisingStart %v\n", result)
		} else {
			ble.Emit(Event{Name: "advertisingStart"})
		}

	case 17: // advertising stop
		result := args.MustGetInt("kCBMsgArgResult")
		if result != 0 {
			log.Printf("event: error in advertisingStop %v\n", result)
		} else {
			ble.Emit(Event{Name: "advertisingStop"})
		}

	case 37: // discover
		advdata := args.MustGetDict("kCBMsgArgAdvertisementData")
		if len(advdata) == 0 {
			//log.Println("event: discover with no advertisment data")
			break
		}

		deviceUuid := args.MustGetUUID("kCBMsgArgDeviceUUID")

		advertisement := Advertisement{
			LocalName:        advdata.GetString("kCBAdvDataLocalName", args.GetString("kCBMsgArgName", "")),
			TxPowerLevel:     advdata.GetInt("kCBAdvDataTxPowerLevel", 0),
			ManufacturerData: advdata.GetBytes("kCBAdvDataManufacturerData", nil),
			ServiceData:      []ServiceData{},
			ServiceUuids:     []string{},
		}

		rssi := args.GetInt("kCBMsgArgRssi", 0)

		if uuids, ok := advdata["kCBAdvDataServiceUUIDs"]; ok {
			for _, uuid := range uuids.(array) {
				advertisement.ServiceUuids = append(advertisement.ServiceUuids, fmt.Sprintf("%x", uuid))
			}
		}

		if data, ok := advdata["kCBAdvDataServiceData"]; ok {
			sdata := data.(array)

			for i := 0; i < len(sdata); i += 2 {
				sd := ServiceData{
					Uuid: fmt.Sprintf("%x", sdata[i+0].([]byte)),
					Data: sdata[i+1].([]byte),
				}

				advertisement.ServiceData = append(advertisement.ServiceData, sd)
			}
		}

		pid := deviceUuid.String()
		p := ble.peripherals[pid]
		emit := ble.allowDuplicates || p == nil

		if p == nil {
			// add new peripheral
			p = &Peripheral{
				Uuid:          deviceUuid,
				Advertisement: advertisement,
				Rssi:          rssi,
				Services:      map[interface{}]*ServiceHandle{},
			}

			ble.peripherals[pid] = p
		} else {
			// update peripheral
			p.Advertisement = advertisement
			p.Rssi = rssi
		}

		if emit {
			ble.Emit(Event{Name: "discover", DeviceUUID: deviceUuid, Peripheral: *p})
		}

	case 38: // connect
		deviceUuid := args.MustGetUUID("kCBMsgArgDeviceUUID")
		ble.Emit(Event{Name: "connect", DeviceUUID: deviceUuid})

	case 40: // disconnect
		deviceUuid := args.MustGetUUID("kCBMsgArgDeviceUUID")
		ble.Emit(Event{Name: "disconnect", DeviceUUID: deviceUuid})

	case 54: // rssiUpdate
		deviceUuid := args.MustGetUUID("kCBMsgArgDeviceUUID")
		rssi := args.MustGetInt("kCBMsgArgData")

		if p, ok := ble.peripherals[deviceUuid.String()]; ok {
			p.Rssi = rssi
			ble.Emit(Event{Name: "rssiUpdate", DeviceUUID: deviceUuid, Peripheral: *p})
		}

	case 55: // serviceDiscover
		deviceUuid := args.MustGetUUID("kCBMsgArgDeviceUUID")
		servicesUuids := []string{}
		servicesHandles := map[interface{}]*ServiceHandle{}

		if dservices, ok := args["kCBMsgArgServices"]; ok {
			for _, s := range dservices.(array) {
				service := s.(dict)
				serviceHandle := ServiceHandle{
					Uuid:            service.MustGetHexBytes("kCBMsgArgUUID"),
					startHandle:     service.MustGetInt("kCBMsgArgServiceStartHandle"),
					endHandle:       service.MustGetInt("kCBMsgArgServiceEndHandle"),
					Characteristics: map[interface{}]*ServiceCharacteristic{}}

				if nameType, ok := knownServices[serviceHandle.Uuid]; ok {
					serviceHandle.Name = nameType.Name
					serviceHandle.Type = nameType.Type
				}

				servicesHandles[serviceHandle.Uuid] = &serviceHandle
				servicesHandles[serviceHandle.startHandle] = &serviceHandle

				servicesUuids = append(servicesUuids, serviceHandle.Uuid)
			}
		}

		if p, ok := ble.peripherals[deviceUuid.String()]; ok {
			p.Services = servicesHandles
			ble.Emit(Event{Name: "servicesDiscover", DeviceUUID: deviceUuid, Peripheral: *p})
		}

	case 63: // characteristicsDiscover
		deviceUuid := args.MustGetUUID("kCBMsgArgDeviceUUID")
		serviceStartHandle := args.MustGetInt("kCBMsgArgServiceStartHandle")

		if p, ok := ble.peripherals[deviceUuid.String()]; ok {
			service := p.Services[serviceStartHandle]

			//result := args.MustGetInt("kCBMsgArgResult")

			for _, c := range args.MustGetArray("kCBMsgArgCharacteristics") {
				cDict := c.(dict)

				characteristic := ServiceCharacteristic{
					Uuid:        cDict.MustGetHexBytes("kCBMsgArgUUID"),
					Handle:      cDict.MustGetInt("kCBMsgArgCharacteristicHandle"),
					ValueHandle: cDict.MustGetInt("kCBMsgArgCharacteristicValueHandle"),
					Descriptors: map[interface{}]*CharacteristicDescriptor{},
				}

				if nameType, ok := knownCharacteristics[characteristic.Uuid]; ok {
					characteristic.Name = nameType.Name
					characteristic.Type = nameType.Type
				}

				properties := cDict.MustGetInt("kCBMsgArgCharacteristicProperties")

				if (properties & 0x01) != 0 {
					characteristic.Properties |= Broadcast
				}

				if (properties & 0x02) != 0 {
					characteristic.Properties |= Read
				}

				if (properties & 0x04) != 0 {
					characteristic.Properties |= WriteWithoutResponse
				}

				if (properties & 0x08) != 0 {
					characteristic.Properties |= Write
				}

				if (properties & 0x10) != 0 {
					characteristic.Properties |= Notify
				}

				if (properties & 0x20) != 0 {
					characteristic.Properties |= Indicate
				}

				if (properties & 0x40) != 0 {
					characteristic.Properties |= AuthenticatedSignedWrites
				}

				if (properties & 0x80) != 0 {
					characteristic.Properties |= ExtendedProperties
				}

				if service != nil {
					service.Characteristics[characteristic.Uuid] = &characteristic
					service.Characteristics[characteristic.Handle] = &characteristic
					service.Characteristics[characteristic.ValueHandle] = &characteristic
				}
			}

			if service != nil {
				ble.Emit(Event{Name: "characteristicsDiscover", DeviceUUID: deviceUuid, ServiceUuid: service.Uuid, Peripheral: *p})
			} else {
				log.Println("no service", serviceStartHandle)
			}
		} else {
			log.Println("no peripheral", deviceUuid)
		}

	case 75: // descriptorsDiscover
		deviceUuid := args.MustGetUUID("kCBMsgArgDeviceUUID")
		characteristicsHandle := args.MustGetInt("kCBMsgArgCharacteristicHandle")
		//result := args.MustGetInt("kCBMsgArgResult")

		if p, ok := ble.peripherals[deviceUuid.String()]; ok {
			for _, s := range p.Services {
				if c, ok := s.Characteristics[characteristicsHandle]; ok {
					for _, d := range args.MustGetArray("kCBMsgArgDescriptors") {
						dDict := d.(dict)
						descriptor := CharacteristicDescriptor{
							Uuid:   dDict.MustGetHexBytes("kCBMsgArgUUID"),
							Handle: dDict.MustGetInt("kCBMsgArgDescriptorHandle"),
						}

						c.Descriptors[descriptor.Uuid] = &descriptor
						c.Descriptors[descriptor.Handle] = &descriptor
					}

					ble.Emit(Event{Name: "descriptorsDiscover", DeviceUUID: deviceUuid, ServiceUuid: s.Uuid, CharacteristicUuid: c.Uuid, Peripheral: *p})
					break
				}
			}
		} else {
			log.Println("no peripheral", deviceUuid)
		}

	case 70:
		deviceUuid := args.MustGetUUID("kCBMsgArgDeviceUUID")
		characteristicsHandle := args.MustGetInt("kCBMsgArgCharacteristicHandle")
		//result := args.MustGetInt("kCBMsgArgResult")
		isNotification := args.GetInt("kCBMsgArgIsNotification", 0) != 0
		data := args.MustGetBytes("kCBMsgArgData")

		if p, ok := ble.peripherals[deviceUuid.String()]; ok {
			for _, s := range p.Services {
				if c, ok := s.Characteristics[characteristicsHandle]; ok {
					ble.Emit(Event{Name: "read", DeviceUUID: deviceUuid, ServiceUuid: s.Uuid, CharacteristicUuid: c.Uuid, Peripheral: *p, Data: data, IsNotification: isNotification})
					break
				}
			}
		}
	}
}

// send a message to Blued
func (ble *BLE) sendCBMsg(id int, args dict) {
	message := dict{"kCBMsgId": id, "kCBMsgArgs": args}
	if ble.verbose {
		log.Printf("sendCBMsg %#v\n", message)
	}

	C.XpcSendMessage(ble.conn, goToXpc(message), true, ble.verbose == true)
}

// initialize BLE
func (ble *BLE) Init() {
	ble.sendCBMsg(1, dict{"kCBMsgArgName": fmt.Sprintf("goble-%v", time.Now().Unix()),
		"kCBMsgArgOptions": dict{"kCBInitOptionShowPowerAlert": 0}, "kCBMsgArgType": 0})
}

// start advertising
func (ble *BLE) StartAdvertising(name string, serviceUuids []UUID) {
	uuids := make([][]byte, len(serviceUuids))
	for i, uuid := range serviceUuids {
		uuids[i] = []byte(uuid[:])
	}
	ble.sendCBMsg(8, dict{"kCBAdvDataLocalName": name, "kCBAdvDataServiceUUIDs": uuids})
}

// start advertising as IBeacon (raw data)
func (ble *BLE) StartAdvertisingIBeaconData(data []byte) {
	ble.sendCBMsg(8, dict{"kCBAdvDataAppleBeaconKey": data})
}

// start advertising as IBeacon
func (ble *BLE) StartAdvertisingIBeacon(uuid UUID, major, minor uint16, measuredPower int8) {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.BigEndian, uuid[:])
	binary.Write(buf, binary.BigEndian, major)
	binary.Write(buf, binary.BigEndian, minor)
	binary.Write(buf, binary.BigEndian, measuredPower)

	ble.sendCBMsg(8, dict{"kCBAdvDataAppleBeaconKey": buf.Bytes()})
}

// stop advertising
func (ble *BLE) StopAdvertising() {
	ble.sendCBMsg(9, nil)
}

// start scanning
func (ble *BLE) StartScanning(serviceUuids []UUID, allowDuplicates bool) {
	uuids := []string{}

	for _, uuid := range serviceUuids {
		uuids = append(uuids, uuid.String())
	}

	args := dict{"kCBMsgArgUUIDs": uuids}
	if allowDuplicates {
		args["kCBMsgArgOptions"] = dict{"kCBScanOptionAllowDuplicates": 1}
	} else {
		args["kCBMsgArgOptions"] = dict{}
	}

	ble.allowDuplicates = allowDuplicates
	ble.sendCBMsg(29, args)
}

// stop scanning
func (ble *BLE) StopScanning() {
	ble.sendCBMsg(30, nil)
}

// connect
func (ble *BLE) Connect(deviceUuid UUID) {
	uuid := deviceUuid.String()
	if p, ok := ble.peripherals[uuid]; ok {
		ble.sendCBMsg(31, dict{"kCBMsgArgOptions": dict{"kCBConnectOptionNotifyOnDisconnection": 1}, "kCBMsgArgDeviceUUID": p.Uuid})
	} else {
		log.Println("no peripheral", deviceUuid)
	}
}

// disconnect
func (ble *BLE) Disconnect(deviceUuid UUID) {
	uuid := deviceUuid.String()
	if p, ok := ble.peripherals[uuid]; ok {
		ble.sendCBMsg(32, dict{"kCBMsgArgDeviceUUID": p.Uuid})
	} else {
		log.Println("no peripheral", deviceUuid)
	}
}

// update rssi
func (ble *BLE) UpdateRssi(deviceUuid UUID) {
	uuid := deviceUuid.String()
	if p, ok := ble.peripherals[uuid]; ok {
		ble.sendCBMsg(43, dict{"kCBMsgArgDeviceUUID": p.Uuid})
	} else {
		log.Println("no peripheral", deviceUuid)
	}
}

// discover services
func (ble *BLE) DiscoverServices(deviceUuid UUID, uuids []UUID) {
	sUuid := deviceUuid.String()
	if p, ok := ble.peripherals[sUuid]; ok {
		sUuids := make([]string, len(uuids))
		for i, uuid := range uuids {
			sUuids[i] = uuid.String() // uuids may be a list of []byte (2 bytes)
		}
		ble.sendCBMsg(44, dict{"kCBMsgArgDeviceUUID": p.Uuid, "kCBMsgArgUUIDs": sUuids})
	} else {
		log.Println("no peripheral", deviceUuid)
	}
}

// discover characteristics
func (ble *BLE) DiscoverCharacterstics(deviceUuid UUID, serviceUuid string, characteristicUuids []string) {
	sUuid := deviceUuid.String()
	if p, ok := ble.peripherals[sUuid]; ok {
		cUuids := make([]string, len(characteristicUuids))
		for i, cuuid := range characteristicUuids {
			cUuids[i] = cuuid // characteristicUuids may be a list of []byte (2 bytes)
		}

		ble.sendCBMsg(61, dict{
			"kCBMsgArgDeviceUUID":         p.Uuid,
			"kCBMsgArgServiceStartHandle": p.Services[serviceUuid].startHandle,
			"kCBMsgArgServiceEndHandle":   p.Services[serviceUuid].endHandle,
			"kCBMsgArgUUIDs":              cUuids,
		})

	} else {
		log.Println("no peripheral", deviceUuid)
	}
}

// discover descriptors
func (ble *BLE) DiscoverDescriptors(deviceUuid UUID, serviceUuid, characteristicUuid string) {
	sUuid := deviceUuid.String()
	if p, ok := ble.peripherals[sUuid]; ok {
		s := p.Services[serviceUuid]
		c := s.Characteristics[characteristicUuid]

		ble.sendCBMsg(69, dict{
			"kCBMsgArgDeviceUUID":                p.Uuid,
			"kCBMsgArgCharacteristicHandle":      c.Handle,
			"kCBMsgArgCharacteristicValueHandle": c.ValueHandle,
		})
	} else {
		log.Println("no peripheral", deviceUuid)
	}
}

// read
func (ble *BLE) Read(deviceUuid UUID, serviceUuid, characteristicUuid string) {
	sUuid := deviceUuid.String()
	if p, ok := ble.peripherals[sUuid]; ok {
		s := p.Services[serviceUuid]
		c := s.Characteristics[characteristicUuid]

		ble.sendCBMsg(64, dict{
			"kCBMsgArgDeviceUUID":                p.Uuid,
			"kCBMsgArgCharacteristicHandle":      c.Handle,
			"kCBMsgArgCharacteristicValueHandle": c.ValueHandle,
		})
	} else {
		log.Println("no peripheral", deviceUuid)
	}
}

// remove all services
func (ble *BLE) RemoveServices() {
	ble.sendCBMsg(12, nil)
}

// set services
func (ble *BLE) SetServices(services []Service) {
	ble.sendCBMsg(12, nil) // remove all services
	ble.attributes = array{nil}

	attributeId := 1

	for _, service := range services {
		arg := dict{
			"kCBMsgArgAttributeID":     attributeId,
			"kCBMsgArgAttributeIDs":    []int{},
			"kCBMsgArgCharacteristics": nil,
			"kCBMsgArgType":            1, // 1 => primary, 0 => excluded
			"kCBMsgArgUUID":            service.uuid.String(),
		}

		ble.attributes = append(ble.attributes, service)
		ble.lastServiceAttributeId = attributeId
		attributeId += 1

		characteristics := array{}

		for _, characteristic := range service.characteristics {
			properties := 0
			permissions := 0

			if Read&characteristic.properties != 0 {
				properties |= 0x02

				if Read&characteristic.secure != 0 {
					permissions |= 0x04
				} else {
					permissions |= 0x01
				}
			}

			if WriteWithoutResponse&characteristic.properties != 0 {
				properties |= 0x04

				if WriteWithoutResponse&characteristic.secure != 0 {
					permissions |= 0x08
				} else {
					permissions |= 0x02
				}
			}

			if Write&characteristic.properties != 0 {
				properties |= 0x08

				if WriteWithoutResponse&characteristic.secure != 0 {
					permissions |= 0x08
				} else {
					permissions |= 0x02
				}
			}

			if Notify&characteristic.properties != 0 {
				if Notify&characteristic.secure != 0 {
					properties |= 0x100
				} else {
					properties |= 0x10
				}
			}

			if Indicate&characteristic.properties != 0 {
				if Indicate&characteristic.secure != 0 {
					properties |= 0x200
				} else {
					properties |= 0x20
				}
			}

			descriptors := array{}
			for _, descriptor := range characteristic.descriptors {
				descriptors = append(descriptors, dict{"kCBMsgArgData": descriptor.value, "kCBMsgArgUUID": descriptor.uuid.String()})
			}

			characteristicArg := dict{
				"kCBMsgArgAttributeID":              attributeId,
				"kCBMsgArgAttributePermissions":     permissions,
				"kCBMsgArgCharacteristicProperties": properties,
				"kCBMsgArgData":                     characteristic.value,
				"kCBMsgArgDescriptors":              descriptors,
				"kCBMsgArgUUID":                     characteristic.uuid.String(),
			}

			ble.attributes = append(ble.attributes, characteristic)
			characteristics = append(characteristics, characteristicArg)

			attributeId += 1
		}

		arg["kCBMsgArgCharacteristics"] = characteristics
		ble.sendCBMsg(10, arg) // remove all services
	}
}
