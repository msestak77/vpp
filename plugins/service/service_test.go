// Copyright (c) 2018 Cisco and/or its affiliates.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at:
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package service

import (
	. "github.com/onsi/gomega"
	"net"
	"strconv"
	"testing"

	"github.com/ligato/cn-infra/logging"
	"github.com/ligato/cn-infra/logging/logrus"
	"github.com/ligato/vpp-agent/plugins/defaultplugins/common/model/nat"

	. "github.com/contiv/vpp/mock/contiv"
	. "github.com/contiv/vpp/mock/datasync"
	. "github.com/contiv/vpp/mock/defaultplugins"
	. "github.com/contiv/vpp/mock/natplugin"
	. "github.com/contiv/vpp/mock/servicelabel"

	"github.com/contiv/vpp/mock/localclient"
	contivplugin "github.com/contiv/vpp/plugins/contiv"
	svc_configurator "github.com/contiv/vpp/plugins/service/configurator"
	svc_processor "github.com/contiv/vpp/plugins/service/processor"

	nodemodel "github.com/contiv/vpp/plugins/contiv/model/node"
	epmodel "github.com/contiv/vpp/plugins/ksr/model/endpoints"
	podmodel "github.com/contiv/vpp/plugins/ksr/model/pod"
	svcmodel "github.com/contiv/vpp/plugins/ksr/model/service"
)

const (
	masterLabel = "master"
	workerLabel = "worker"

	// master
	mainIfName      = "GbE"
	OtherIfName     = "GbE2"
	OtherIfName2    = "GbE3"
	vxlanIfName     = "VXLAN-BVI"
	hostInterIfName = "VPP-Host"
	nodeIP          = "192.168.16.10"
	mgmtIP          = "172.30.1.1"
	otherIfIP       = "192.168.17.10"
	otherIfIP2      = "192.168.18.10"
	nodePrefix      = "/24"
	defaultGwIP     = "192.168.16.1"
	defaultGwIP2    = "192.168.17.1"

	// worker
	workerIP     = "192.168.16.20"
	workerMgmtIP = "172.30.1.2"

	podNetwork = "10.1.0.0/16"
	namespace1 = "default"
	namespace2 = "another-ns"
)

var (
	pod1 = podmodel.ID{Name: "pod1", Namespace: namespace1}
	pod2 = podmodel.ID{Name: "pod2", Namespace: namespace1}
	pod3 = podmodel.ID{Name: "pod3", Namespace: namespace2}

	pod1IP = "10.1.1.3"
	pod2IP = "10.1.1.4"
	pod3IP = "10.2.1.1"

	pod1If = "master-tap1"
	pod2If = "master-tap2"

	pod1Model = &podmodel.Pod{
		Name:      pod1.Name,
		Namespace: pod1.Namespace,
		IpAddress: pod1IP,
	}

	pod2Model = &podmodel.Pod{
		Name:      pod2.Name,
		Namespace: pod2.Namespace,
		IpAddress: pod2IP,
	}

	pod3Model = &podmodel.Pod{
		Name:      pod3.Name,
		Namespace: pod3.Namespace,
		IpAddress: pod3IP,
	}
)

var (
	keyPrefixes = []string{epmodel.KeyPrefix(), podmodel.KeyPrefix(), svcmodel.KeyPrefix(), contivplugin.AllocatedIDsKeyPrefix}
)

func TestResyncAndSingleService(t *testing.T) {
	RegisterTestingT(t)
	logger := logrus.DefaultLogger()
	logger.SetLevel(logging.DebugLevel)
	logger.Debug("TestSomething")

	// Prepare mocks.
	//  -> Contiv plugin
	contiv := NewMockContiv()
	contiv.SetNatExternalTraffic(true)
	contiv.SetNodeIP(nodeIP + nodePrefix)
	contiv.SetDefaultGatewayIP(net.ParseIP(defaultGwIP))
	contiv.SetMainPhysicalIfName(mainIfName)
	contiv.SetVxlanBVIIfName(vxlanIfName)
	contiv.SetHostInterconnectIfName(hostInterIfName)
	contiv.SetPodNetwork(podNetwork)
	contiv.SetPodIfName(pod1, pod1If)
	contiv.SetPodIfName(pod2, pod2If)

	// -> NAT plugin
	natPlugin := NewMockNatPlugin(logger)

	// -> localclient
	txnTracker := localclient.NewTxnTracker(natPlugin.ApplyTxn)

	// -> default VPP plugins
	vppPlugins := NewMockVppPlugin()
	vppPlugins.SetNat44Dnat(&nat.Nat44DNat{})

	// -> service label
	serviceLabel := NewMockServiceLabel()
	serviceLabel.SetAgentLabel(masterLabel)

	// -> datasync
	datasync := NewMockDataSync()

	// Prepare configurator.
	configurator := &svc_configurator.ServiceConfigurator{
		Deps: svc_configurator.Deps{
			Log:           logger,
			VPP:           vppPlugins,
			NATTxnFactory: txnTracker.NewLinuxDataChangeTxn,
		},
	}

	// Prepare processor.
	processor := &svc_processor.ServiceProcessor{
		Deps: svc_processor.Deps{
			Log:          logger,
			ServiceLabel: serviceLabel,
			Contiv:       contiv,
			Configurator: configurator,
		},
	}

	Expect(configurator.Init()).To(BeNil())
	Expect(processor.Init()).To(BeNil())

	// Test resync with empty VPP configuration.
	resyncEv := datasync.Resync(keyPrefixes...)
	Expect(processor.Resync(resyncEv)).To(BeNil())

	Expect(natPlugin.IsForwardingEnabled()).To(BeTrue())

	Expect(natPlugin.AddressPoolSize()).To(Equal(1))
	Expect(natPlugin.PoolContainsAddress(nodeIP)).To(BeTrue())

	Expect(natPlugin.NumOfIfsWithFeatures()).To(Equal(3))
	Expect(natPlugin.GetInterfaceFeatures(mainIfName)).To(Equal(NewNatFeatures(OUTPUT_OUT)))
	Expect(natPlugin.GetInterfaceFeatures(vxlanIfName)).To(Equal(NewNatFeatures(IN, OUT)))
	Expect(natPlugin.GetInterfaceFeatures(hostInterIfName)).To(Equal(NewNatFeatures(IN, OUT)))

	Expect(natPlugin.NumOfStaticMappings()).To(Equal(0))
	Expect(natPlugin.NumOfIdentityMappings()).To(Equal(0))

	// Add service metadata.
	service1 := &svcmodel.Service{
		Name:                  "service1",
		Namespace:             namespace1,
		ServiceType:           "ClusterIP",
		ExternalTrafficPolicy: "Cluster",
		ClusterIp:             "10.96.0.1",
		ExternalIps:           []string{"20.20.20.20"},
		Port: []*svcmodel.Service_ServicePort{
			{
				Name:     "http",
				Protocol: "TCP",
				Port:     80,
				NodePort: 0,
			},
		},
	}

	dataChange1 := datasync.Put(svcmodel.Key(service1.Name, service1.Namespace), service1)
	Expect(processor.Update(dataChange1)).To(BeNil())

	// No change in the NAT configuration.
	Expect(natPlugin.IsForwardingEnabled()).To(BeTrue())

	Expect(natPlugin.AddressPoolSize()).To(Equal(1))
	Expect(natPlugin.PoolContainsAddress(nodeIP)).To(BeTrue())

	Expect(natPlugin.NumOfIfsWithFeatures()).To(Equal(3))
	Expect(natPlugin.GetInterfaceFeatures(mainIfName)).To(Equal(NewNatFeatures(OUTPUT_OUT)))
	Expect(natPlugin.GetInterfaceFeatures(vxlanIfName)).To(Equal(NewNatFeatures(IN, OUT)))
	Expect(natPlugin.GetInterfaceFeatures(hostInterIfName)).To(Equal(NewNatFeatures(IN, OUT)))

	Expect(natPlugin.NumOfStaticMappings()).To(Equal(0))
	Expect(natPlugin.NumOfIdentityMappings()).To(Equal(0))

	// Add pods.
	dataChange2 := datasync.Put(podmodel.Key(pod1.Name, pod1.Namespace), pod1Model)
	Expect(processor.Update(dataChange2)).To(BeNil())
	dataChange3 := datasync.Put(podmodel.Key(pod2.Name, pod2.Namespace), pod2Model)
	Expect(processor.Update(dataChange3)).To(BeNil())

	// First check what should not have changed.
	Expect(natPlugin.IsForwardingEnabled()).To(BeTrue())
	Expect(natPlugin.AddressPoolSize()).To(Equal(1))
	Expect(natPlugin.PoolContainsAddress(nodeIP)).To(BeTrue())
	Expect(natPlugin.NumOfStaticMappings()).To(Equal(0))
	Expect(natPlugin.NumOfIdentityMappings()).To(Equal(0))

	// Interface attaching pods should have NAT/OUT enabled.
	Expect(natPlugin.NumOfIfsWithFeatures()).To(Equal(5))
	Expect(natPlugin.GetInterfaceFeatures(mainIfName)).To(Equal(NewNatFeatures(OUTPUT_OUT)))
	Expect(natPlugin.GetInterfaceFeatures(vxlanIfName)).To(Equal(NewNatFeatures(IN, OUT)))
	Expect(natPlugin.GetInterfaceFeatures(hostInterIfName)).To(Equal(NewNatFeatures(IN, OUT)))
	Expect(natPlugin.GetInterfaceFeatures(pod1If)).To(Equal(NewNatFeatures(OUT)))
	Expect(natPlugin.GetInterfaceFeatures(pod2If)).To(Equal(NewNatFeatures(OUT)))

	// Add endpoints.
	eps1 := &epmodel.Endpoints{
		Name:      "service1",
		Namespace: namespace1,
		EndpointSubsets: []*epmodel.EndpointSubset{
			{
				Addresses: []*epmodel.EndpointSubset_EndpointAddress{
					{
						Ip:       pod1IP,
						NodeName: masterLabel,
						TargetRef: &epmodel.ObjectReference{
							Kind:      "Pod",
							Namespace: pod1.Namespace,
							Name:      pod1.Name,
						},
					},
					{
						Ip:       pod2IP,
						NodeName: masterLabel,
						TargetRef: &epmodel.ObjectReference{
							Kind:      "Pod",
							Namespace: pod2.Namespace,
							Name:      pod2.Name,
						},
					},
				},
				Ports: []*epmodel.EndpointSubset_EndpointPort{
					{
						Name:     "http",
						Port:     8080,
						Protocol: "TCP",
					},
				},
			},
		},
	}

	dataChange4 := datasync.Put(epmodel.Key(eps1.Name, eps1.Namespace), eps1)
	Expect(processor.Update(dataChange4)).To(BeNil())

	// First check what should not have changed.
	Expect(natPlugin.IsForwardingEnabled()).To(BeTrue())
	Expect(natPlugin.AddressPoolSize()).To(Equal(1))
	Expect(natPlugin.PoolContainsAddress(nodeIP)).To(BeTrue())
	Expect(natPlugin.NumOfIdentityMappings()).To(Equal(0))

	// New interfaces with enabled NAT features.
	Expect(natPlugin.NumOfIfsWithFeatures()).To(Equal(5))
	Expect(natPlugin.GetInterfaceFeatures(mainIfName)).To(Equal(NewNatFeatures(OUTPUT_OUT)))
	Expect(natPlugin.GetInterfaceFeatures(vxlanIfName)).To(Equal(NewNatFeatures(IN, OUT)))
	Expect(natPlugin.GetInterfaceFeatures(hostInterIfName)).To(Equal(NewNatFeatures(IN, OUT)))
	Expect(natPlugin.GetInterfaceFeatures(pod1If)).To(Equal(NewNatFeatures(IN, OUT)))
	Expect(natPlugin.GetInterfaceFeatures(pod2If)).To(Equal(NewNatFeatures(IN, OUT)))

	// New static mappings.
	Expect(natPlugin.NumOfStaticMappings()).To(Equal(2))
	staticMapping1 := &StaticMapping{
		ExternalIP:   net.ParseIP("10.96.0.1"),
		ExternalPort: 80,
		Protocol:     svc_configurator.TCP,
		Locals: []*Local{
			{
				IP:          net.ParseIP(pod1IP),
				Port:        8080,
				Probability: 2,
			},
			{
				IP:          net.ParseIP(pod2IP),
				Port:        8080,
				Probability: 2,
			},
		},
	}
	Expect(natPlugin.HasStaticMapping(staticMapping1)).To(BeTrue())
	staticMapping2 := staticMapping1.Copy()
	staticMapping2.ExternalIP = net.ParseIP("20.20.20.20")
	Expect(natPlugin.HasStaticMapping(staticMapping2)).To(BeTrue())

	// Change port number for pod2.
	eps2 := &epmodel.Endpoints{
		Name:      "service1",
		Namespace: namespace1,
		EndpointSubsets: []*epmodel.EndpointSubset{
			{
				Addresses: []*epmodel.EndpointSubset_EndpointAddress{
					{
						Ip:       pod1IP,
						NodeName: masterLabel,
						TargetRef: &epmodel.ObjectReference{
							Kind:      "Pod",
							Namespace: pod1.Namespace,
							Name:      pod1.Name,
						},
					},
				},
				Ports: []*epmodel.EndpointSubset_EndpointPort{
					{
						Name:     "http",
						Port:     8080,
						Protocol: "TCP",
					},
				},
			},
			{
				Addresses: []*epmodel.EndpointSubset_EndpointAddress{
					{
						Ip:       pod2IP,
						NodeName: masterLabel,
						TargetRef: &epmodel.ObjectReference{
							Kind:      "Pod",
							Namespace: pod2.Namespace,
							Name:      pod2.Name,
						},
					},
				},
				Ports: []*epmodel.EndpointSubset_EndpointPort{
					{
						Name:     "http",
						Port:     9080, // 8080 -> 9080
						Protocol: "TCP",
					},
				},
			},
		},
	}

	dataChange5 := datasync.Put(epmodel.Key(eps2.Name, eps2.Namespace), eps2)
	Expect(processor.Update(dataChange5)).To(BeNil())

	// First check what should not have changed.
	Expect(natPlugin.IsForwardingEnabled()).To(BeTrue())
	Expect(natPlugin.AddressPoolSize()).To(Equal(1))
	Expect(natPlugin.PoolContainsAddress(nodeIP)).To(BeTrue())
	Expect(natPlugin.NumOfIdentityMappings()).To(Equal(0))

	// New static mappings.
	Expect(natPlugin.NumOfStaticMappings()).To(Equal(2))
	staticMapping1.Locals[1].Port = 9080
	staticMapping2.Locals[1].Port = 9080
	Expect(natPlugin.HasStaticMapping(staticMapping1)).To(BeTrue())
	Expect(natPlugin.HasStaticMapping(staticMapping2)).To(BeTrue())

	// Finally remove the service.
	dataChange6 := datasync.Delete(svcmodel.Key(service1.Name, service1.Namespace))
	Expect(processor.Update(dataChange6)).To(BeNil())

	// NAT configuration without the service.
	Expect(natPlugin.IsForwardingEnabled()).To(BeTrue())
	Expect(natPlugin.AddressPoolSize()).To(Equal(1))
	Expect(natPlugin.PoolContainsAddress(nodeIP)).To(BeTrue())
	Expect(natPlugin.NumOfStaticMappings()).To(Equal(0))
	Expect(natPlugin.NumOfIdentityMappings()).To(Equal(0))
	Expect(natPlugin.NumOfIfsWithFeatures()).To(Equal(5))
	Expect(natPlugin.GetInterfaceFeatures(mainIfName)).To(Equal(NewNatFeatures(OUTPUT_OUT)))
	Expect(natPlugin.GetInterfaceFeatures(vxlanIfName)).To(Equal(NewNatFeatures(IN, OUT)))
	Expect(natPlugin.GetInterfaceFeatures(hostInterIfName)).To(Equal(NewNatFeatures(IN, OUT)))
	Expect(natPlugin.GetInterfaceFeatures(pod1If)).To(Equal(NewNatFeatures(OUT)))
	Expect(natPlugin.GetInterfaceFeatures(pod2If)).To(Equal(NewNatFeatures(OUT)))

	// Cleanup
	Expect(processor.Close()).To(BeNil())
	Expect(configurator.Close()).To(BeNil())
}

func TestMultipleServicesWithMultiplePortsAndResync(t *testing.T) {
	RegisterTestingT(t)
	logger := logrus.DefaultLogger()
	logger.SetLevel(logging.DebugLevel)
	logger.Debug("TestMultipleServicesWithMultiplePortsAndResync")

	// Prepare mocks.
	//  -> Contiv plugin
	contiv := NewMockContiv()
	contiv.SetNatExternalTraffic(true)
	contiv.SetNodeIP(nodeIP + nodePrefix)
	contiv.SetDefaultGatewayIP(net.ParseIP(defaultGwIP))
	contiv.SetMainPhysicalIfName(mainIfName)
	contiv.SetVxlanBVIIfName(vxlanIfName)
	contiv.SetHostInterconnectIfName(hostInterIfName)
	contiv.SetPodNetwork(podNetwork)
	contiv.SetPodIfName(pod1, pod1If)
	contiv.SetPodIfName(pod2, pod2If)

	// -> NAT plugin
	natPlugin := NewMockNatPlugin(logger)

	// -> localclient
	txnTracker := localclient.NewTxnTracker(natPlugin.ApplyTxn)

	// -> default VPP plugins
	vppPlugins := NewMockVppPlugin()
	vppPlugins.SetNat44Dnat(&nat.Nat44DNat{})

	// -> service label
	serviceLabel := NewMockServiceLabel()
	serviceLabel.SetAgentLabel(masterLabel)

	// -> datasync
	datasync := NewMockDataSync()

	// Prepare configurator.
	configurator := &svc_configurator.ServiceConfigurator{
		Deps: svc_configurator.Deps{
			Log:           logger,
			VPP:           vppPlugins,
			NATTxnFactory: txnTracker.NewLinuxDataChangeTxn,
		},
	}

	// Prepare processor.
	processor := &svc_processor.ServiceProcessor{
		Deps: svc_processor.Deps{
			Log:          logger,
			VPP:          vppPlugins,
			ServiceLabel: serviceLabel,
			Contiv:       contiv,
			Configurator: configurator,
		},
	}

	// Initialize and resync.
	Expect(configurator.Init()).To(BeNil())
	Expect(processor.Init()).To(BeNil())
	resyncEv := datasync.Resync(keyPrefixes...)
	Expect(processor.Resync(resyncEv)).To(BeNil())

	// Add pods.
	dataChange1 := datasync.Put(podmodel.Key(pod1.Name, pod1.Namespace), pod1Model)
	Expect(processor.Update(dataChange1)).To(BeNil())
	dataChange2 := datasync.Put(podmodel.Key(pod2.Name, pod2.Namespace), pod2Model)
	Expect(processor.Update(dataChange2)).To(BeNil())
	dataChange3 := datasync.Put(podmodel.Key(pod3.Name, pod3.Namespace), pod3Model)
	Expect(processor.Update(dataChange3)).To(BeNil())

	// Service1: http + https with nodePort.
	service1 := &svcmodel.Service{
		Name:                  "service1",
		Namespace:             namespace1,
		ServiceType:           "ClusterIP",
		ExternalTrafficPolicy: "Cluster",
		ClusterIp:             "10.96.0.1",
		ExternalIps:           []string{"20.20.20.20"},
		Port: []*svcmodel.Service_ServicePort{
			{
				Name:     "http",
				Protocol: "TCP",
				Port:     80,
				NodePort: 0,
			},
			{
				Name:     "https",
				Protocol: "TCP",
				Port:     443,
				NodePort: 30443,
			},
		},
	}

	// Service2: DNS.
	service2 := &svcmodel.Service{
		Name:                  "service2",
		Namespace:             namespace2,
		ServiceType:           "ClusterIP",
		ExternalTrafficPolicy: "Cluster",
		ClusterIp:             "10.96.0.10",
		Port: []*svcmodel.Service_ServicePort{
			{
				Name:     "dns-tcp",
				Protocol: "TCP",
				Port:     53,
				NodePort: 0,
			},
			{
				Name:     "dns-udp",
				Protocol: "UDP",
				Port:     53,
				NodePort: 0,
			},
		},
	}

	dataChange4 := datasync.Put(svcmodel.Key(service1.Name, service1.Namespace), service1)
	Expect(processor.Update(dataChange4)).To(BeNil())
	dataChange5 := datasync.Put(svcmodel.Key(service2.Name, service2.Namespace), service2)
	Expect(processor.Update(dataChange5)).To(BeNil())

	// Check NAT configuration.
	Expect(natPlugin.IsForwardingEnabled()).To(BeTrue())

	Expect(natPlugin.AddressPoolSize()).To(Equal(1))
	Expect(natPlugin.PoolContainsAddress(nodeIP)).To(BeTrue())

	Expect(natPlugin.NumOfIfsWithFeatures()).To(Equal(5))
	Expect(natPlugin.GetInterfaceFeatures(mainIfName)).To(Equal(NewNatFeatures(OUTPUT_OUT)))
	Expect(natPlugin.GetInterfaceFeatures(vxlanIfName)).To(Equal(NewNatFeatures(IN, OUT)))
	Expect(natPlugin.GetInterfaceFeatures(hostInterIfName)).To(Equal(NewNatFeatures(IN, OUT)))
	Expect(natPlugin.GetInterfaceFeatures(pod1If)).To(Equal(NewNatFeatures(OUT)))
	Expect(natPlugin.GetInterfaceFeatures(pod2If)).To(Equal(NewNatFeatures(OUT)))

	Expect(natPlugin.NumOfStaticMappings()).To(Equal(0))
	Expect(natPlugin.NumOfIdentityMappings()).To(Equal(0))

	// Add endpoints.
	eps1 := &epmodel.Endpoints{
		Name:      "service1",
		Namespace: namespace1,
		EndpointSubsets: []*epmodel.EndpointSubset{
			{
				Addresses: []*epmodel.EndpointSubset_EndpointAddress{
					{
						Ip:       pod1IP,
						NodeName: masterLabel,
						TargetRef: &epmodel.ObjectReference{
							Kind:      "Pod",
							Namespace: pod1.Namespace,
							Name:      pod1.Name,
						},
					},
					{
						Ip:       pod2IP,
						NodeName: masterLabel,
						TargetRef: &epmodel.ObjectReference{
							Kind:      "Pod",
							Namespace: pod2.Namespace,
							Name:      pod2.Name,
						},
					},
				},
				Ports: []*epmodel.EndpointSubset_EndpointPort{
					{
						Name:     "http",
						Port:     8080,
						Protocol: "TCP",
					},
					{
						Name:     "https",
						Port:     8443,
						Protocol: "TCP",
					},
				},
			},
		},
	}

	eps2 := &epmodel.Endpoints{
		Name:      "service2",
		Namespace: namespace2,
		EndpointSubsets: []*epmodel.EndpointSubset{
			{
				Addresses: []*epmodel.EndpointSubset_EndpointAddress{
					{
						Ip:       pod1IP,
						NodeName: masterLabel,
						TargetRef: &epmodel.ObjectReference{
							Kind:      "Pod",
							Namespace: pod1.Namespace,
							Name:      pod1.Name,
						},
					},
				},
				Ports: []*epmodel.EndpointSubset_EndpointPort{
					{
						Name:     "dns-tcp",
						Port:     10053,
						Protocol: "TCP",
					},
					{
						Name:     "dns-udp",
						Port:     10053,
						Protocol: "UDP",
					},
				},
			},
			{
				Addresses: []*epmodel.EndpointSubset_EndpointAddress{
					{
						Ip:       pod3IP,
						NodeName: workerLabel,
						TargetRef: &epmodel.ObjectReference{
							Kind:      "Pod",
							Namespace: pod1.Namespace,
							Name:      pod1.Name,
						},
					},
				},
				Ports: []*epmodel.EndpointSubset_EndpointPort{
					{
						Name:     "dns-tcp",
						Port:     53,
						Protocol: "TCP",
					},
					{
						Name:     "dns-udp",
						Port:     53,
						Protocol: "UDP",
					},
				},
			},
		},
	}

	dataChange6 := datasync.Put(epmodel.Key(eps1.Name, eps1.Namespace), eps1)
	Expect(processor.Update(dataChange6)).To(BeNil())
	dataChange7 := datasync.Put(epmodel.Key(eps2.Name, eps2.Namespace), eps2)
	Expect(processor.Update(dataChange7)).To(BeNil())

	// First check what should not have changed.
	Expect(natPlugin.IsForwardingEnabled()).To(BeTrue())
	Expect(natPlugin.AddressPoolSize()).To(Equal(1))
	Expect(natPlugin.PoolContainsAddress(nodeIP)).To(BeTrue())
	Expect(natPlugin.NumOfIdentityMappings()).To(Equal(0))

	// New interfaces with enabled NAT features.
	Expect(natPlugin.NumOfIfsWithFeatures()).To(Equal(5))
	Expect(natPlugin.GetInterfaceFeatures(mainIfName)).To(Equal(NewNatFeatures(OUTPUT_OUT)))
	Expect(natPlugin.GetInterfaceFeatures(vxlanIfName)).To(Equal(NewNatFeatures(IN, OUT)))
	Expect(natPlugin.GetInterfaceFeatures(hostInterIfName)).To(Equal(NewNatFeatures(IN, OUT)))
	Expect(natPlugin.GetInterfaceFeatures(pod1If)).To(Equal(NewNatFeatures(IN, OUT)))
	Expect(natPlugin.GetInterfaceFeatures(pod2If)).To(Equal(NewNatFeatures(IN, OUT)))

	// New static mappings.
	// -> service 1
	staticMappingHTTP := &StaticMapping{
		ExternalIP:   net.ParseIP("10.96.0.1"),
		ExternalPort: 80,
		Protocol:     svc_configurator.TCP,
		Locals: []*Local{
			{
				IP:          net.ParseIP(pod1IP),
				Port:        8080,
				Probability: 2,
			},
			{
				IP:          net.ParseIP(pod2IP),
				Port:        8080,
				Probability: 2,
			},
		},
	}
	staticMappingHTTPS := &StaticMapping{
		ExternalIP:   net.ParseIP("10.96.0.1"),
		ExternalPort: 443,
		Protocol:     svc_configurator.TCP,
		Locals: []*Local{
			{
				IP:          net.ParseIP(pod1IP),
				Port:        8443,
				Probability: 2,
			},
			{
				IP:          net.ParseIP(pod2IP),
				Port:        8443,
				Probability: 2,
			},
		},
	}
	Expect(natPlugin.HasStaticMapping(staticMappingHTTP)).To(BeTrue())
	Expect(natPlugin.HasStaticMapping(staticMappingHTTPS)).To(BeTrue())
	staticMappingHTTP2 := staticMappingHTTP.Copy()
	staticMappingHTTP2.ExternalIP = net.ParseIP("20.20.20.20")
	staticMappingHTTPS2 := staticMappingHTTPS.Copy()
	staticMappingHTTPS2.ExternalIP = net.ParseIP("20.20.20.20")
	Expect(natPlugin.HasStaticMapping(staticMappingHTTP2)).To(BeTrue())
	Expect(natPlugin.HasStaticMapping(staticMappingHTTPS2)).To(BeTrue())

	// -> service 2
	staticMappingDNSTCP := &StaticMapping{
		ExternalIP:   net.ParseIP("10.96.0.10"),
		ExternalPort: 53,
		Protocol:     svc_configurator.TCP,
		Locals: []*Local{
			{
				IP:          net.ParseIP(pod1IP),
				Port:        10053,
				Probability: 2,
			},
			{
				IP:          net.ParseIP(pod3IP),
				Port:        53,
				Probability: 1,
			},
		},
	}
	staticMappingDNSUDP := &StaticMapping{
		ExternalIP:   net.ParseIP("10.96.0.10"),
		ExternalPort: 53,
		Protocol:     svc_configurator.UDP,
		Locals: []*Local{
			{
				IP:          net.ParseIP(pod1IP),
				Port:        10053,
				Probability: 2,
			},
			{
				IP:          net.ParseIP(pod3IP),
				Port:        53,
				Probability: 1,
			},
		},
	}
	Expect(natPlugin.HasStaticMapping(staticMappingDNSTCP)).To(BeTrue())
	Expect(natPlugin.HasStaticMapping(staticMappingDNSUDP)).To(BeTrue())

	// -> total
	Expect(natPlugin.NumOfStaticMappings()).To(Equal(6))

	// Propagate NodeIP and Node Mgmt IP of the master.
	masterNode := &nodemodel.NodeInfo{
		Id:                  1,
		Name:                masterLabel,
		IpAddress:           nodeIP + nodePrefix,
		ManagementIpAddress: mgmtIP,
	}

	dataChange8 := datasync.Put(contivplugin.AllocatedIDsKeyPrefix+strconv.FormatUint(uint64(masterNode.Id), 10), masterNode)
	Expect(processor.Update(dataChange8)).To(BeNil())

	// First check what should not have changed.
	Expect(natPlugin.IsForwardingEnabled()).To(BeTrue())
	Expect(natPlugin.AddressPoolSize()).To(Equal(1))
	Expect(natPlugin.PoolContainsAddress(nodeIP)).To(BeTrue())
	Expect(natPlugin.NumOfIdentityMappings()).To(Equal(0))
	Expect(natPlugin.NumOfIfsWithFeatures()).To(Equal(5))
	Expect(natPlugin.GetInterfaceFeatures(mainIfName)).To(Equal(NewNatFeatures(OUTPUT_OUT)))
	Expect(natPlugin.GetInterfaceFeatures(vxlanIfName)).To(Equal(NewNatFeatures(IN, OUT)))
	Expect(natPlugin.GetInterfaceFeatures(hostInterIfName)).To(Equal(NewNatFeatures(IN, OUT)))
	Expect(natPlugin.GetInterfaceFeatures(pod1If)).To(Equal(NewNatFeatures(IN, OUT)))
	Expect(natPlugin.GetInterfaceFeatures(pod2If)).To(Equal(NewNatFeatures(IN, OUT)))
	Expect(natPlugin.HasStaticMapping(staticMappingHTTP)).To(BeTrue())
	Expect(natPlugin.HasStaticMapping(staticMappingHTTPS)).To(BeTrue())
	Expect(natPlugin.HasStaticMapping(staticMappingHTTP2)).To(BeTrue())
	Expect(natPlugin.HasStaticMapping(staticMappingHTTPS2)).To(BeTrue())
	Expect(natPlugin.HasStaticMapping(staticMappingDNSTCP)).To(BeTrue())
	Expect(natPlugin.HasStaticMapping(staticMappingDNSUDP)).To(BeTrue())

	// New static mappings for the https nodeport.
	staticMappingHTTPSNodeIP := &StaticMapping{
		ExternalIP:   net.ParseIP(nodeIP),
		ExternalPort: 30443,
		Protocol:     svc_configurator.TCP,
		Locals: []*Local{
			{
				IP:          net.ParseIP(pod1IP),
				Port:        8443,
				Probability: 2,
			},
			{
				IP:          net.ParseIP(pod2IP),
				Port:        8443,
				Probability: 2,
			},
		},
	}
	staticMappingHTTPSNodeMgmtIP := &StaticMapping{
		ExternalIP:   net.ParseIP(mgmtIP),
		ExternalPort: 30443,
		Protocol:     svc_configurator.TCP,
		Locals: []*Local{
			{
				IP:          net.ParseIP(pod1IP),
				Port:        8443,
				Probability: 2,
			},
			{
				IP:          net.ParseIP(pod2IP),
				Port:        8443,
				Probability: 2,
			},
		},
	}

	Expect(natPlugin.HasStaticMapping(staticMappingHTTPSNodeIP)).To(BeTrue())
	Expect(natPlugin.HasStaticMapping(staticMappingHTTPSNodeMgmtIP)).To(BeTrue())
	Expect(natPlugin.NumOfStaticMappings()).To(Equal(8))

	// Propagate NodeIP and Node Mgmt IP of the worker.
	workerNode := &nodemodel.NodeInfo{
		Id:                  2,
		Name:                workerLabel,
		IpAddress:           workerIP + nodePrefix,
		ManagementIpAddress: workerMgmtIP,
	}

	dataChange9 := datasync.Put(contivplugin.AllocatedIDsKeyPrefix+strconv.FormatUint(uint64(workerNode.Id), 10), workerNode)
	Expect(processor.Update(dataChange9)).To(BeNil())

	// First check what should not have changed.
	Expect(natPlugin.IsForwardingEnabled()).To(BeTrue())
	Expect(natPlugin.AddressPoolSize()).To(Equal(1))
	Expect(natPlugin.PoolContainsAddress(nodeIP)).To(BeTrue())
	Expect(natPlugin.NumOfIdentityMappings()).To(Equal(0))
	Expect(natPlugin.NumOfIfsWithFeatures()).To(Equal(5))
	Expect(natPlugin.GetInterfaceFeatures(mainIfName)).To(Equal(NewNatFeatures(OUTPUT_OUT)))
	Expect(natPlugin.GetInterfaceFeatures(vxlanIfName)).To(Equal(NewNatFeatures(IN, OUT)))
	Expect(natPlugin.GetInterfaceFeatures(hostInterIfName)).To(Equal(NewNatFeatures(IN, OUT)))
	Expect(natPlugin.GetInterfaceFeatures(pod1If)).To(Equal(NewNatFeatures(IN, OUT)))
	Expect(natPlugin.GetInterfaceFeatures(pod2If)).To(Equal(NewNatFeatures(IN, OUT)))
	Expect(natPlugin.HasStaticMapping(staticMappingHTTP)).To(BeTrue())
	Expect(natPlugin.HasStaticMapping(staticMappingHTTPS)).To(BeTrue())
	Expect(natPlugin.HasStaticMapping(staticMappingHTTP2)).To(BeTrue())
	Expect(natPlugin.HasStaticMapping(staticMappingHTTPS2)).To(BeTrue())
	Expect(natPlugin.HasStaticMapping(staticMappingDNSTCP)).To(BeTrue())
	Expect(natPlugin.HasStaticMapping(staticMappingDNSUDP)).To(BeTrue())
	Expect(natPlugin.HasStaticMapping(staticMappingHTTPSNodeIP)).To(BeTrue())
	Expect(natPlugin.HasStaticMapping(staticMappingHTTPSNodeMgmtIP)).To(BeTrue())

	// New static mappings for the https nodeport - worker.
	staticMappingHTTPSWorkerNodeIP := staticMappingHTTPSNodeIP.Copy()
	staticMappingHTTPSWorkerNodeIP.ExternalIP = net.ParseIP(workerIP)
	staticMappingHTTPSWorkerNodeMgmtIP := staticMappingHTTPSNodeMgmtIP.Copy()
	staticMappingHTTPSWorkerNodeMgmtIP.ExternalIP = net.ParseIP(workerMgmtIP)
	Expect(natPlugin.HasStaticMapping(staticMappingHTTPSWorkerNodeIP)).To(BeTrue())
	Expect(natPlugin.HasStaticMapping(staticMappingHTTPSWorkerNodeMgmtIP)).To(BeTrue())
	Expect(natPlugin.NumOfStaticMappings()).To(Equal(10))

	// Remove worker mgmt IP.
	workerNode = &nodemodel.NodeInfo{
		Id:                  2,
		Name:                workerLabel,
		IpAddress:           workerIP + nodePrefix,
		ManagementIpAddress: "", /* removed */
	}

	dataChange10 := datasync.Put(contivplugin.AllocatedIDsKeyPrefix+strconv.FormatUint(uint64(workerNode.Id), 10), workerNode)
	Expect(processor.Update(dataChange10)).To(BeNil())

	// Check that the static mapping for worker mgmt IP was removed.
	Expect(natPlugin.HasStaticMapping(staticMappingHTTP)).To(BeTrue())
	Expect(natPlugin.HasStaticMapping(staticMappingHTTPS)).To(BeTrue())
	Expect(natPlugin.HasStaticMapping(staticMappingHTTP2)).To(BeTrue())
	Expect(natPlugin.HasStaticMapping(staticMappingHTTPS2)).To(BeTrue())
	Expect(natPlugin.HasStaticMapping(staticMappingDNSTCP)).To(BeTrue())
	Expect(natPlugin.HasStaticMapping(staticMappingDNSUDP)).To(BeTrue())
	Expect(natPlugin.HasStaticMapping(staticMappingHTTPSNodeIP)).To(BeTrue())
	Expect(natPlugin.HasStaticMapping(staticMappingHTTPSNodeMgmtIP)).To(BeTrue())
	Expect(natPlugin.HasStaticMapping(staticMappingHTTPSWorkerNodeIP)).To(BeTrue())
	Expect(natPlugin.NumOfStaticMappings()).To(Equal(9))

	// Remove worker node completely.
	dataChange11 := datasync.Delete(contivplugin.AllocatedIDsKeyPrefix + strconv.FormatUint(uint64(workerNode.Id), 10))
	Expect(processor.Update(dataChange11)).To(BeNil())

	// Check that the static mapping for worker IP was removed.
	Expect(natPlugin.HasStaticMapping(staticMappingHTTP)).To(BeTrue())
	Expect(natPlugin.HasStaticMapping(staticMappingHTTPS)).To(BeTrue())
	Expect(natPlugin.HasStaticMapping(staticMappingHTTP2)).To(BeTrue())
	Expect(natPlugin.HasStaticMapping(staticMappingHTTPS2)).To(BeTrue())
	Expect(natPlugin.HasStaticMapping(staticMappingDNSTCP)).To(BeTrue())
	Expect(natPlugin.HasStaticMapping(staticMappingDNSUDP)).To(BeTrue())
	Expect(natPlugin.HasStaticMapping(staticMappingHTTPSNodeIP)).To(BeTrue())
	Expect(natPlugin.HasStaticMapping(staticMappingHTTPSNodeMgmtIP)).To(BeTrue())
	Expect(natPlugin.NumOfStaticMappings()).To(Equal(8))

	// Simulate Resync.
	// -> cache mocked VPP configuration
	vppPlugins.SetNat44Global(natPlugin.DumpNat44Global())
	vppPlugins.SetNat44Dnat(natPlugin.DumpNat44DNat())
	// -> simulate restart of the service plugin components
	configurator = &svc_configurator.ServiceConfigurator{
		Deps: svc_configurator.Deps{
			Log:           logger,
			VPP:           vppPlugins,
			NATTxnFactory: txnTracker.NewLinuxDataChangeTxn,
		},
	}
	processor = &svc_processor.ServiceProcessor{
		Deps: svc_processor.Deps{
			Log:          logger,
			VPP:          vppPlugins,
			ServiceLabel: serviceLabel,
			Contiv:       contiv,
			Configurator: configurator,
		},
	}
	// -> let's simulate that during downtime the  service1 was removed
	datasync.Delete(svcmodel.Key(service1.Name, service1.Namespace))
	// -> initialize and resync
	Expect(configurator.Init()).To(BeNil())
	Expect(processor.Init()).To(BeNil())
	resyncEv2 := datasync.Resync(keyPrefixes...)
	Expect(processor.Resync(resyncEv2)).To(BeNil())

	// Check NAT configuration.
	Expect(natPlugin.IsForwardingEnabled()).To(BeTrue())

	Expect(natPlugin.AddressPoolSize()).To(Equal(1))
	Expect(natPlugin.PoolContainsAddress(nodeIP)).To(BeTrue())

	Expect(natPlugin.NumOfIfsWithFeatures()).To(Equal(5))
	Expect(natPlugin.GetInterfaceFeatures(mainIfName)).To(Equal(NewNatFeatures(OUTPUT_OUT)))
	Expect(natPlugin.GetInterfaceFeatures(vxlanIfName)).To(Equal(NewNatFeatures(IN, OUT)))
	Expect(natPlugin.GetInterfaceFeatures(hostInterIfName)).To(Equal(NewNatFeatures(IN, OUT)))
	Expect(natPlugin.GetInterfaceFeatures(pod1If)).To(Equal(NewNatFeatures(IN, OUT)))
	Expect(natPlugin.GetInterfaceFeatures(pod2If)).To(Equal(NewNatFeatures(OUT)))

	Expect(natPlugin.NumOfIdentityMappings()).To(Equal(0))
	Expect(natPlugin.HasStaticMapping(staticMappingDNSTCP)).To(BeTrue())
	Expect(natPlugin.HasStaticMapping(staticMappingDNSUDP)).To(BeTrue())
	Expect(natPlugin.NumOfStaticMappings()).To(Equal(2))

	// Cleanup
	Expect(processor.Close()).To(BeNil())
	Expect(configurator.Close()).To(BeNil())
}

func TestWithVXLANButNoGateway(t *testing.T) {
	RegisterTestingT(t)
	logger := logrus.DefaultLogger()
	logger.SetLevel(logging.DebugLevel)
	logger.Debug("TestWithVXLANButNoGateway")

	// Prepare mocks.
	//  -> Contiv plugin
	contiv := NewMockContiv()
	contiv.SetNatExternalTraffic(true)
	contiv.SetNodeIP(nodeIP + nodePrefix)
	contiv.SetMainPhysicalIfName(mainIfName)
	contiv.SetVxlanBVIIfName(vxlanIfName)
	contiv.SetHostInterconnectIfName(hostInterIfName)
	contiv.SetPodNetwork(podNetwork)
	contiv.SetPodIfName(pod1, pod1If)
	contiv.SetPodIfName(pod2, pod2If)

	// -> NAT plugin
	natPlugin := NewMockNatPlugin(logger)

	// -> localclient
	txnTracker := localclient.NewTxnTracker(natPlugin.ApplyTxn)

	// -> default VPP plugins
	vppPlugins := NewMockVppPlugin()
	vppPlugins.SetNat44Dnat(&nat.Nat44DNat{})

	// -> service label
	serviceLabel := NewMockServiceLabel()
	serviceLabel.SetAgentLabel(masterLabel)

	// -> datasync
	datasync := NewMockDataSync()

	// Prepare configurator.
	configurator := &svc_configurator.ServiceConfigurator{
		Deps: svc_configurator.Deps{
			Log:           logger,
			VPP:           vppPlugins,
			NATTxnFactory: txnTracker.NewLinuxDataChangeTxn,
		},
	}

	// Prepare processor.
	processor := &svc_processor.ServiceProcessor{
		Deps: svc_processor.Deps{
			Log:          logger,
			VPP:          vppPlugins,
			ServiceLabel: serviceLabel,
			Contiv:       contiv,
			Configurator: configurator,
		},
	}

	Expect(configurator.Init()).To(BeNil())
	Expect(processor.Init()).To(BeNil())

	// Resync from empty VPP.
	resyncEv := datasync.Resync(keyPrefixes...)
	Expect(processor.Resync(resyncEv)).To(BeNil())

	// Check that SNAT is NOT configured.
	Expect(natPlugin.IsForwardingEnabled()).To(BeTrue())

	Expect(natPlugin.PoolContainsAddress(nodeIP)).To(BeFalse())
	Expect(natPlugin.AddressPoolSize()).To(Equal(0))

	Expect(natPlugin.NumOfIfsWithFeatures()).To(Equal(3))
	Expect(natPlugin.GetInterfaceFeatures(mainIfName)).To(Equal(NewNatFeatures(OUT)))
	Expect(natPlugin.GetInterfaceFeatures(vxlanIfName)).To(Equal(NewNatFeatures(IN, OUT)))
	Expect(natPlugin.GetInterfaceFeatures(hostInterIfName)).To(Equal(NewNatFeatures(IN, OUT)))

	Expect(natPlugin.NumOfStaticMappings()).To(Equal(0))
	Expect(natPlugin.NumOfIdentityMappings()).To(Equal(0))

	// Cleanup
	Expect(processor.Close()).To(BeNil())
	Expect(configurator.Close()).To(BeNil())
}

func TestWithoutVXLAN(t *testing.T) {
	RegisterTestingT(t)
	logger := logrus.DefaultLogger()
	logger.SetLevel(logging.DebugLevel)
	logger.Debug("TestWithoutVXLAN")

	// Prepare mocks.
	//  -> Contiv plugin
	contiv := NewMockContiv()
	contiv.SetNatExternalTraffic(true)
	contiv.SetNodeIP(nodeIP + nodePrefix)
	contiv.SetDefaultGatewayIP(net.ParseIP(defaultGwIP))
	contiv.SetMainPhysicalIfName(mainIfName)
	contiv.SetHostInterconnectIfName(hostInterIfName)
	contiv.SetPodNetwork(podNetwork)
	contiv.SetPodIfName(pod1, pod1If)
	contiv.SetPodIfName(pod2, pod2If)

	// -> NAT plugin
	natPlugin := NewMockNatPlugin(logger)

	// -> localclient
	txnTracker := localclient.NewTxnTracker(natPlugin.ApplyTxn)

	// -> default VPP plugins
	vppPlugins := NewMockVppPlugin()
	vppPlugins.SetNat44Dnat(&nat.Nat44DNat{})

	// -> service label
	serviceLabel := NewMockServiceLabel()
	serviceLabel.SetAgentLabel(masterLabel)

	// -> datasync
	datasync := NewMockDataSync()

	// Prepare configurator.
	configurator := &svc_configurator.ServiceConfigurator{
		Deps: svc_configurator.Deps{
			Log:           logger,
			VPP:           vppPlugins,
			NATTxnFactory: txnTracker.NewLinuxDataChangeTxn,
		},
	}

	// Prepare processor.
	processor := &svc_processor.ServiceProcessor{
		Deps: svc_processor.Deps{
			Log:          logger,
			VPP:          vppPlugins,
			ServiceLabel: serviceLabel,
			Contiv:       contiv,
			Configurator: configurator,
		},
	}

	Expect(configurator.Init()).To(BeNil())
	Expect(processor.Init()).To(BeNil())

	// Resync from empty VPP.
	resyncEv := datasync.Resync(keyPrefixes...)
	Expect(processor.Resync(resyncEv)).To(BeNil())

	// Check that SNAT is NOT configured.
	Expect(natPlugin.IsForwardingEnabled()).To(BeTrue())

	Expect(natPlugin.PoolContainsAddress(nodeIP)).To(BeFalse())
	Expect(natPlugin.AddressPoolSize()).To(Equal(0))

	Expect(natPlugin.NumOfIfsWithFeatures()).To(Equal(2))
	Expect(natPlugin.GetInterfaceFeatures(mainIfName)).To(Equal(NewNatFeatures(IN, OUT)))
	Expect(natPlugin.GetInterfaceFeatures(hostInterIfName)).To(Equal(NewNatFeatures(IN, OUT)))

	Expect(natPlugin.NumOfStaticMappings()).To(Equal(0))
	Expect(natPlugin.NumOfIdentityMappings()).To(Equal(0))

	// Cleanup
	Expect(processor.Close()).To(BeNil())
	Expect(configurator.Close()).To(BeNil())
}

func TestWithOtherInterfaces(t *testing.T) {
	RegisterTestingT(t)
	logger := logrus.DefaultLogger()
	logger.SetLevel(logging.DebugLevel)
	logger.Debug("TestWithOtherInterfaces")

	// Prepare mocks.
	//  -> Contiv plugin
	contiv := NewMockContiv()
	contiv.SetNatExternalTraffic(true)
	contiv.SetNodeIP(nodeIP + nodePrefix)
	contiv.SetDefaultGatewayIP(net.ParseIP(defaultGwIP2))
	contiv.SetMainPhysicalIfName(mainIfName)
	contiv.SetOtherPhysicalIfNames([]string{OtherIfName, OtherIfName2})
	contiv.SetVxlanBVIIfName(vxlanIfName)
	contiv.SetHostInterconnectIfName(hostInterIfName)
	contiv.SetPodNetwork(podNetwork)
	contiv.SetPodIfName(pod1, pod1If)
	contiv.SetPodIfName(pod2, pod2If)

	// -> NAT plugin
	natPlugin := NewMockNatPlugin(logger)

	// -> localclient
	txnTracker := localclient.NewTxnTracker(natPlugin.ApplyTxn)

	// -> default VPP plugins
	vppPlugins := NewMockVppPlugin()
	vppPlugins.AddInterface(OtherIfName, 1, otherIfIP+nodePrefix)
	vppPlugins.AddInterface(OtherIfName2, 2, otherIfIP2+nodePrefix)
	vppPlugins.SetNat44Dnat(&nat.Nat44DNat{})

	// -> service label
	serviceLabel := NewMockServiceLabel()
	serviceLabel.SetAgentLabel(masterLabel)

	// -> datasync
	datasync := NewMockDataSync()

	// Prepare configurator.
	configurator := &svc_configurator.ServiceConfigurator{
		Deps: svc_configurator.Deps{
			Log:           logger,
			VPP:           vppPlugins,
			NATTxnFactory: txnTracker.NewLinuxDataChangeTxn,
		},
	}

	// Prepare processor.
	processor := &svc_processor.ServiceProcessor{
		Deps: svc_processor.Deps{
			Log:          logger,
			VPP:          vppPlugins,
			ServiceLabel: serviceLabel,
			Contiv:       contiv,
			Configurator: configurator,
		},
	}

	Expect(configurator.Init()).To(BeNil())
	Expect(processor.Init()).To(BeNil())

	// Resync from empty VPP.
	resyncEv := datasync.Resync(keyPrefixes...)
	Expect(processor.Resync(resyncEv)).To(BeNil())

	// Check that SNAT is configured.
	Expect(natPlugin.IsForwardingEnabled()).To(BeTrue())

	Expect(natPlugin.AddressPoolSize()).To(Equal(1))
	Expect(natPlugin.PoolContainsAddress(otherIfIP)).To(BeTrue())

	Expect(natPlugin.NumOfIfsWithFeatures()).To(Equal(5))
	Expect(natPlugin.GetInterfaceFeatures(mainIfName)).To(Equal(NewNatFeatures(OUT)))
	Expect(natPlugin.GetInterfaceFeatures(vxlanIfName)).To(Equal(NewNatFeatures(IN, OUT)))
	Expect(natPlugin.GetInterfaceFeatures(hostInterIfName)).To(Equal(NewNatFeatures(IN, OUT)))
	Expect(natPlugin.GetInterfaceFeatures(OtherIfName)).To(Equal(NewNatFeatures(OUTPUT_OUT)))
	Expect(natPlugin.GetInterfaceFeatures(OtherIfName2)).To(Equal(NewNatFeatures(OUT)))

	Expect(natPlugin.NumOfStaticMappings()).To(Equal(0))
	Expect(natPlugin.NumOfIdentityMappings()).To(Equal(0))

	// Cleanup
	Expect(processor.Close()).To(BeNil())
	Expect(configurator.Close()).To(BeNil())
}

func TestServiceUpdates(t *testing.T) {
	RegisterTestingT(t)
	logger := logrus.DefaultLogger()
	logger.SetLevel(logging.DebugLevel)
	logger.Debug("TestServiceUpdates")

	// Prepare mocks.
	//  -> Contiv plugin
	contiv := NewMockContiv()
	contiv.SetNatExternalTraffic(true)
	contiv.SetNodeIP(nodeIP + nodePrefix)
	contiv.SetDefaultGatewayIP(net.ParseIP(defaultGwIP))
	contiv.SetMainPhysicalIfName(mainIfName)
	contiv.SetVxlanBVIIfName(vxlanIfName)
	contiv.SetHostInterconnectIfName(hostInterIfName)
	contiv.SetPodNetwork(podNetwork)
	contiv.SetPodIfName(pod1, pod1If)
	contiv.SetPodIfName(pod2, pod2If)

	// -> NAT plugin
	natPlugin := NewMockNatPlugin(logger)

	// -> localclient
	txnTracker := localclient.NewTxnTracker(natPlugin.ApplyTxn)

	// -> default VPP plugins
	vppPlugins := NewMockVppPlugin()
	vppPlugins.SetNat44Dnat(&nat.Nat44DNat{})

	// -> service label
	serviceLabel := NewMockServiceLabel()
	serviceLabel.SetAgentLabel(masterLabel)

	// -> datasync
	datasync := NewMockDataSync()

	// Prepare configurator.
	configurator := &svc_configurator.ServiceConfigurator{
		Deps: svc_configurator.Deps{
			Log:           logger,
			VPP:           vppPlugins,
			NATTxnFactory: txnTracker.NewLinuxDataChangeTxn,
		},
	}

	// Prepare processor.
	processor := &svc_processor.ServiceProcessor{
		Deps: svc_processor.Deps{
			Log:          logger,
			VPP:          vppPlugins,
			ServiceLabel: serviceLabel,
			Contiv:       contiv,
			Configurator: configurator,
		},
	}

	// Initialize and resync.
	Expect(configurator.Init()).To(BeNil())
	Expect(processor.Init()).To(BeNil())
	resyncEv := datasync.Resync(keyPrefixes...)
	Expect(processor.Resync(resyncEv)).To(BeNil())

	// Add pods.
	dataChange1 := datasync.Put(podmodel.Key(pod1.Name, pod1.Namespace), pod1Model)
	Expect(processor.Update(dataChange1)).To(BeNil())
	dataChange2 := datasync.Put(podmodel.Key(pod2.Name, pod2.Namespace), pod2Model)
	Expect(processor.Update(dataChange2)).To(BeNil())
	dataChange3 := datasync.Put(podmodel.Key(pod3.Name, pod3.Namespace), pod3Model)
	Expect(processor.Update(dataChange3)).To(BeNil())

	// Service1: http only (not https yet).
	service1 := &svcmodel.Service{
		Name:                  "service1",
		Namespace:             namespace1,
		ServiceType:           "ClusterIP",
		ExternalTrafficPolicy: "Cluster",
		ClusterIp:             "10.96.0.1",
		ExternalIps:           []string{"20.20.20.20"},
		Port: []*svcmodel.Service_ServicePort{
			{
				Name:     "http",
				Protocol: "TCP",
				Port:     80,
				NodePort: 0,
			},
		},
	}

	dataChange4 := datasync.Put(svcmodel.Key(service1.Name, service1.Namespace), service1)
	Expect(processor.Update(dataChange4)).To(BeNil())

	// Add endpoints.
	eps1 := &epmodel.Endpoints{
		Name:      "service1",
		Namespace: namespace1,
		EndpointSubsets: []*epmodel.EndpointSubset{
			{
				Addresses: []*epmodel.EndpointSubset_EndpointAddress{
					{
						Ip:       pod1IP,
						NodeName: masterLabel,
						TargetRef: &epmodel.ObjectReference{
							Kind:      "Pod",
							Namespace: pod1.Namespace,
							Name:      pod1.Name,
						},
					},
					{
						Ip:       pod2IP,
						NodeName: masterLabel,
						TargetRef: &epmodel.ObjectReference{
							Kind:      "Pod",
							Namespace: pod2.Namespace,
							Name:      pod2.Name,
						},
					},
					{
						Ip:       pod3IP,
						NodeName: workerLabel,
						TargetRef: &epmodel.ObjectReference{
							Kind:      "Pod",
							Namespace: pod3.Namespace,
							Name:      pod3.Name,
						},
					},
				},
				Ports: []*epmodel.EndpointSubset_EndpointPort{
					{
						Name:     "http",
						Port:     8080,
						Protocol: "TCP",
					},
				},
			},
		},
	}

	dataChange5 := datasync.Put(epmodel.Key(eps1.Name, eps1.Namespace), eps1)
	Expect(processor.Update(dataChange5)).To(BeNil())

	// Check NAT configuration.
	Expect(natPlugin.IsForwardingEnabled()).To(BeTrue())

	Expect(natPlugin.AddressPoolSize()).To(Equal(1))
	Expect(natPlugin.PoolContainsAddress(nodeIP)).To(BeTrue())

	Expect(natPlugin.NumOfIfsWithFeatures()).To(Equal(5))
	Expect(natPlugin.GetInterfaceFeatures(mainIfName)).To(Equal(NewNatFeatures(OUTPUT_OUT)))
	Expect(natPlugin.GetInterfaceFeatures(vxlanIfName)).To(Equal(NewNatFeatures(IN, OUT)))
	Expect(natPlugin.GetInterfaceFeatures(hostInterIfName)).To(Equal(NewNatFeatures(IN, OUT)))
	Expect(natPlugin.GetInterfaceFeatures(pod1If)).To(Equal(NewNatFeatures(IN, OUT)))
	Expect(natPlugin.GetInterfaceFeatures(pod2If)).To(Equal(NewNatFeatures(IN, OUT)))

	staticMappingHTTP := &StaticMapping{
		ExternalIP:   net.ParseIP("10.96.0.1"),
		ExternalPort: 80,
		Protocol:     svc_configurator.TCP,
		Locals: []*Local{
			{
				IP:          net.ParseIP(pod1IP),
				Port:        8080,
				Probability: 2,
			},
			{
				IP:          net.ParseIP(pod2IP),
				Port:        8080,
				Probability: 2,
			},
			{
				IP:          net.ParseIP(pod3IP),
				Port:        8080,
				Probability: 1,
			},
		},
	}
	Expect(natPlugin.HasStaticMapping(staticMappingHTTP)).To(BeTrue())
	staticMappingHTTP2 := staticMappingHTTP.Copy()
	staticMappingHTTP2.ExternalIP = net.ParseIP("20.20.20.20")
	Expect(natPlugin.HasStaticMapping(staticMappingHTTP2)).To(BeTrue())
	Expect(natPlugin.NumOfStaticMappings()).To(Equal(2))
	Expect(natPlugin.NumOfIdentityMappings()).To(Equal(0))

	// Remove pod2.
	contiv.DeletingPod(pod2)

	// Update endpoints accordingly (also add https port)
	eps1 = &epmodel.Endpoints{
		Name:      "service1",
		Namespace: namespace1,
		EndpointSubsets: []*epmodel.EndpointSubset{
			{
				Addresses: []*epmodel.EndpointSubset_EndpointAddress{
					{
						Ip:       pod1IP,
						NodeName: masterLabel,
						TargetRef: &epmodel.ObjectReference{
							Kind:      "Pod",
							Namespace: pod1.Namespace,
							Name:      pod1.Name,
						},
					},
				},
				Ports: []*epmodel.EndpointSubset_EndpointPort{
					{
						Name:     "http",
						Port:     8080,
						Protocol: "TCP",
					},
					{
						Name:     "https",
						Port:     8443,
						Protocol: "TCP",
					},
				},
			},
			{
				Addresses: []*epmodel.EndpointSubset_EndpointAddress{
					{
						Ip:       pod3IP,
						NodeName: workerLabel,
						TargetRef: &epmodel.ObjectReference{
							Kind:      "Pod",
							Namespace: pod3.Namespace,
							Name:      pod3.Name,
						},
					},
				},
				Ports: []*epmodel.EndpointSubset_EndpointPort{
					{
						Name:     "http",
						Port:     8080,
						Protocol: "TCP",
					},
					{
						Name:     "https",
						Port:     443,
						Protocol: "TCP",
					},
				},
			},
		},
	}

	dataChange7 := datasync.Put(epmodel.Key(eps1.Name, eps1.Namespace), eps1)
	Expect(processor.Update(dataChange7)).To(BeNil())

	// Check NAT configuration.
	Expect(natPlugin.IsForwardingEnabled()).To(BeTrue())

	Expect(natPlugin.AddressPoolSize()).To(Equal(1))
	Expect(natPlugin.PoolContainsAddress(nodeIP)).To(BeTrue())

	Expect(natPlugin.NumOfIfsWithFeatures()).To(Equal(4))
	Expect(natPlugin.GetInterfaceFeatures(mainIfName)).To(Equal(NewNatFeatures(OUTPUT_OUT)))
	Expect(natPlugin.GetInterfaceFeatures(vxlanIfName)).To(Equal(NewNatFeatures(IN, OUT)))
	Expect(natPlugin.GetInterfaceFeatures(hostInterIfName)).To(Equal(NewNatFeatures(IN, OUT)))
	Expect(natPlugin.GetInterfaceFeatures(pod1If)).To(Equal(NewNatFeatures(IN, OUT)))

	staticMappingHTTP = &StaticMapping{
		ExternalIP:   net.ParseIP("10.96.0.1"),
		ExternalPort: 80,
		Protocol:     svc_configurator.TCP,
		Locals: []*Local{
			{
				IP:          net.ParseIP(pod1IP),
				Port:        8080,
				Probability: 2,
			},
			{
				IP:          net.ParseIP(pod3IP),
				Port:        8080,
				Probability: 1,
			},
		},
	}
	Expect(natPlugin.HasStaticMapping(staticMappingHTTP)).To(BeTrue())
	staticMappingHTTP2 = staticMappingHTTP.Copy()
	staticMappingHTTP2.ExternalIP = net.ParseIP("20.20.20.20")
	Expect(natPlugin.HasStaticMapping(staticMappingHTTP2)).To(BeTrue())
	Expect(natPlugin.NumOfStaticMappings()).To(Equal(2))
	Expect(natPlugin.NumOfIdentityMappings()).To(Equal(0))

	// Update service - add https.
	service1 = &svcmodel.Service{
		Name:                  "service1",
		Namespace:             namespace1,
		ServiceType:           "ClusterIP",
		ExternalTrafficPolicy: "Cluster",
		ClusterIp:             "10.96.0.1",
		ExternalIps:           []string{"20.20.20.20"},
		Port: []*svcmodel.Service_ServicePort{
			{
				Name:     "http",
				Protocol: "TCP",
				Port:     80,
				NodePort: 0,
			},
			{
				Name:     "https",
				Protocol: "TCP",
				Port:     443,
				NodePort: 0,
			},
		},
	}

	dataChange8 := datasync.Put(svcmodel.Key(service1.Name, service1.Namespace), service1)
	Expect(processor.Update(dataChange8)).To(BeNil())

	// Check NAT configuration.
	Expect(natPlugin.IsForwardingEnabled()).To(BeTrue())

	Expect(natPlugin.AddressPoolSize()).To(Equal(1))
	Expect(natPlugin.PoolContainsAddress(nodeIP)).To(BeTrue())

	Expect(natPlugin.NumOfIfsWithFeatures()).To(Equal(4))
	Expect(natPlugin.GetInterfaceFeatures(mainIfName)).To(Equal(NewNatFeatures(OUTPUT_OUT)))
	Expect(natPlugin.GetInterfaceFeatures(vxlanIfName)).To(Equal(NewNatFeatures(IN, OUT)))
	Expect(natPlugin.GetInterfaceFeatures(hostInterIfName)).To(Equal(NewNatFeatures(IN, OUT)))
	Expect(natPlugin.GetInterfaceFeatures(pod1If)).To(Equal(NewNatFeatures(IN, OUT)))

	staticMappingHTTPS := &StaticMapping{
		ExternalIP:   net.ParseIP("10.96.0.1"),
		ExternalPort: 443,
		Protocol:     svc_configurator.TCP,
		Locals: []*Local{
			{
				IP:          net.ParseIP(pod1IP),
				Port:        8443,
				Probability: 2,
			},
			{
				IP:          net.ParseIP(pod3IP),
				Port:        443,
				Probability: 1,
			},
		},
	}
	Expect(natPlugin.HasStaticMapping(staticMappingHTTP)).To(BeTrue())
	Expect(natPlugin.HasStaticMapping(staticMappingHTTPS)).To(BeTrue())
	staticMappingHTTPS2 := staticMappingHTTPS.Copy()
	staticMappingHTTPS2.ExternalIP = net.ParseIP("20.20.20.20")
	Expect(natPlugin.HasStaticMapping(staticMappingHTTP2)).To(BeTrue())
	Expect(natPlugin.HasStaticMapping(staticMappingHTTPS2)).To(BeTrue())
	Expect(natPlugin.NumOfStaticMappings()).To(Equal(4))
	Expect(natPlugin.NumOfIdentityMappings()).To(Equal(0))

	// Remove all endpoints.
	contiv.DeletingPod(pod1)
	dataChange9 := datasync.Delete(epmodel.Key(eps1.Name, eps1.Namespace))
	Expect(processor.Update(dataChange9)).To(BeNil())

	// Check NAT configuration.
	Expect(natPlugin.IsForwardingEnabled()).To(BeTrue())

	Expect(natPlugin.AddressPoolSize()).To(Equal(1))
	Expect(natPlugin.PoolContainsAddress(nodeIP)).To(BeTrue())

	Expect(natPlugin.NumOfIfsWithFeatures()).To(Equal(3))
	Expect(natPlugin.GetInterfaceFeatures(mainIfName)).To(Equal(NewNatFeatures(OUTPUT_OUT)))
	Expect(natPlugin.GetInterfaceFeatures(vxlanIfName)).To(Equal(NewNatFeatures(IN, OUT)))
	Expect(natPlugin.GetInterfaceFeatures(hostInterIfName)).To(Equal(NewNatFeatures(IN, OUT)))

	Expect(natPlugin.NumOfStaticMappings()).To(Equal(0))
	Expect(natPlugin.NumOfIdentityMappings()).To(Equal(0))
}
