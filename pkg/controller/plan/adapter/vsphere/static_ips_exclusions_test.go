package vsphere

import (
	"encoding/json"
	"strings"

	k8snet "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1/ref"
	planbase "github.com/kubev2v/forklift/pkg/controller/plan/adapter/base"
	plancontext "github.com/kubev2v/forklift/pkg/controller/plan/context"
	ocpmodel "github.com/kubev2v/forklift/pkg/controller/provider/model/ocp"
	"github.com/kubev2v/forklift/pkg/controller/provider/model/vsphere"
	model "github.com/kubev2v/forklift/pkg/controller/provider/web/vsphere"
	"github.com/kubev2v/forklift/pkg/settings"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	cnv "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	firstStaticIPMAC  = "aa:bb:cc:dd:ee:01"
	secondStaticIPMAC = "aa:bb:cc:dd:ee:02"
)

func staticIPTestVM(nics []vsphere.NIC, guests []vsphere.GuestNetwork) model.VM {
	return model.VM{
		VM1:           model.VM1{VM0: model.VM0{ID: "test-vm-id", Name: "test-vm"}},
		GuestID:       "windows9Guest",
		NICs:          nics,
		GuestNetworks: guests,
	}
}

func staticIPTestBuilder(vm model.VM, pairs []api.NetworkPair, networks map[string]model.Network, excluded []string) *Builder {
	builder := createBuilder()
	builder.Plan.Spec.PreserveStaticIPs = true
	builder.Plan.Spec.VMs[0].ExcludeNICsFromStaticIPs = excluded
	builder.Source.Inventory = &mockInventory{vm: vm, networks: networks}
	builder.Map.Network = &api.NetworkMap{Spec: api.NetworkMapSpec{Map: pairs}}
	builder.Plan.Map.Network = builder.Map.Network
	return builder
}

func staticIPEnvValue(env []core.EnvVar, name string) (string, bool) {
	for _, entry := range env {
		if entry.Name == name {
			return entry.Value, true
		}
	}
	return "", false
}

func staticIPUDNClient() client.Client {
	scheme := runtime.NewScheme()
	Expect(core.AddToScheme(scheme)).To(Succeed())
	Expect(k8snet.AddToScheme(scheme)).To(Succeed())
	config, err := json.Marshal(ocpmodel.NetworkConfig{
		AllowPersistentIPs: true,
		Role:               ocpmodel.RolePrimary,
		Subnets:            "10.220.0.0/24",
		Type:               ocpmodel.OvnOverlayType,
	})
	Expect(err).NotTo(HaveOccurred())

	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&core.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name:   "test",
			Labels: map[string]string{"k8s.ovn.org/primary-user-defined-network": ""},
		}},
		&k8snet.NetworkAttachmentDefinition{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "udn",
				Namespace: "test",
				Labels:    map[string]string{"k8s.ovn.org/user-defined-network": ""},
			},
			Spec: k8snet.NetworkAttachmentDefinitionSpec{Config: string(config)},
		},
	).Build()
}

var _ = Describe("vSphere per-vNIC static IP exclusions", func() {
	It("excludes one NIC on a shared network even when the NetworkMap requests preservation", func() {
		firstGuestMAC := strings.ToUpper(strings.ReplaceAll(firstStaticIPMAC, ":", "-"))
		secondGuestMAC := strings.ToUpper(strings.ReplaceAll(secondStaticIPMAC, ":", "-"))
		vm := staticIPTestVM(
			[]vsphere.NIC{
				{MAC: firstStaticIPMAC, Network: vsphere.Ref{ID: "net-A"}},
				{MAC: secondStaticIPMAC, Network: vsphere.Ref{ID: "net-A"}},
			},
			[]vsphere.GuestNetwork{
				{MAC: firstGuestMAC, IP: "10.0.0.1", Origin: ManualOrigin, PrefixLength: 24},
				{MAC: secondGuestMAC, IP: "10.0.0.2", Origin: ManualOrigin, PrefixLength: 24},
			},
		)
		builder := staticIPTestBuilder(vm, []api.NetworkPair{{
			Source:        api.NetworkSourceRef{Ref: ref.Ref{ID: "net-A"}},
			Destination:   api.DestinationNetwork{Type: Pod},
			NetworkIPMode: api.NetworkIPModePreserve,
		}}, map[string]model.Network{"net-A": {Resource: model.Resource{ID: "net-A"}}}, []string{secondStaticIPMAC})

		env, err := builder.PodEnvironment(ref.Ref{ID: vm.ID}, &core.Secret{})
		Expect(err).NotTo(HaveOccurred())
		staticIPs, found := staticIPEnvValue(env, "V2V_staticIPs")
		Expect(found).To(BeTrue())
		Expect(staticIPs).To(ContainSubstring(firstGuestMAC))
		Expect(staticIPs).NotTo(ContainSubstring(secondGuestMAC))
		_, found = staticIPEnvValue(env, "V2V_preserveStaticIPs")
		Expect(found).To(BeTrue())
	})

	It("continues preserving an unmapped NIC when another NIC is excluded", func() {
		vm := staticIPTestVM(
			[]vsphere.NIC{
				{MAC: firstStaticIPMAC, Network: vsphere.Ref{ID: "net-A"}},
				{MAC: secondStaticIPMAC, Network: vsphere.Ref{ID: "net-B"}},
			},
			[]vsphere.GuestNetwork{
				{MAC: firstStaticIPMAC, IP: "10.0.0.1", Origin: ManualOrigin, PrefixLength: 24},
				{MAC: secondStaticIPMAC, IP: "10.0.0.2", Origin: ManualOrigin, PrefixLength: 24},
			},
		)
		builder := staticIPTestBuilder(vm, []api.NetworkPair{{
			Source:      api.NetworkSourceRef{Ref: ref.Ref{ID: "net-A"}},
			Destination: api.DestinationNetwork{Type: Pod},
		}}, map[string]model.Network{"net-A": {Resource: model.Resource{ID: "net-A"}}}, []string{firstStaticIPMAC})

		env, err := builder.PodEnvironment(ref.Ref{ID: vm.ID}, &core.Secret{})
		Expect(err).NotTo(HaveOccurred())
		staticIPs, found := staticIPEnvValue(env, "V2V_staticIPs")
		Expect(found).To(BeTrue())
		Expect(staticIPs).NotTo(ContainSubstring(firstStaticIPMAC))
		Expect(staticIPs).To(ContainSubstring(secondStaticIPMAC))
		_, found = staticIPEnvValue(env, "V2V_preserveStaticIPs")
		Expect(found).To(BeTrue())
	})

	It("does not revive an unmatched NIC for stale or already-none exclusions", func() {
		vm := staticIPTestVM(
			[]vsphere.NIC{
				{MAC: firstStaticIPMAC, Network: vsphere.Ref{ID: "net-A"}},
				{MAC: secondStaticIPMAC, Network: vsphere.Ref{ID: "net-B"}},
			},
			[]vsphere.GuestNetwork{
				{MAC: firstStaticIPMAC, IP: "10.0.0.1", Origin: ManualOrigin, PrefixLength: 24},
				{MAC: secondStaticIPMAC, IP: "10.0.0.2", Origin: ManualOrigin, PrefixLength: 24},
			},
		)
		builder := staticIPTestBuilder(vm, []api.NetworkPair{{
			Source:        api.NetworkSourceRef{Ref: ref.Ref{ID: "net-A"}},
			Destination:   api.DestinationNetwork{Type: Pod},
			NetworkIPMode: api.NetworkIPModeNone,
		}}, map[string]model.Network{"net-A": {Resource: model.Resource{ID: "net-A"}}}, nil)

		for _, excludedMACs := range [][]string{nil, {"aa:bb:cc:dd:ee:99"}, {firstStaticIPMAC}} {
			builder.Plan.Spec.VMs[0].ExcludeNICsFromStaticIPs = excludedMACs
			env, err := builder.PodEnvironment(ref.Ref{ID: vm.ID}, &core.Secret{})
			Expect(err).NotTo(HaveOccurred())
			_, found := staticIPEnvValue(env, "V2V_staticIPs")
			Expect(found).To(BeFalse(), "exclusions %v must not alter the none mapping", excludedMACs)
			_, found = staticIPEnvValue(env, "V2V_preserveStaticIPs")
			Expect(found).To(BeFalse())
		}
	})

	It("omits static IP environment variables when all NICs are excluded", func() {
		vm := staticIPTestVM(
			[]vsphere.NIC{{MAC: firstStaticIPMAC, Network: vsphere.Ref{ID: "net-A"}}},
			[]vsphere.GuestNetwork{{MAC: firstStaticIPMAC, IP: "10.0.0.1", Origin: ManualOrigin, PrefixLength: 24}},
		)
		builder := staticIPTestBuilder(vm, []api.NetworkPair{{
			Source:        api.NetworkSourceRef{Ref: ref.Ref{ID: "net-A"}},
			Destination:   api.DestinationNetwork{Type: Pod},
			NetworkIPMode: api.NetworkIPModePreserve,
		}}, map[string]model.Network{"net-A": {Resource: model.Resource{ID: "net-A"}}}, []string{firstStaticIPMAC})

		env, err := builder.PodEnvironment(ref.Ref{ID: vm.ID}, &core.Secret{})
		Expect(err).NotTo(HaveOccurred())
		_, found := staticIPEnvValue(env, "V2V_staticIPs")
		Expect(found).To(BeFalse())
		_, found = staticIPEnvValue(env, "V2V_preserveStaticIPs")
		Expect(found).To(BeFalse())
	})

	It("omits excluded NICs from the UDN static IP annotation", func() {
		previous := settings.Settings.StaticUdnIpAddresses
		settings.Settings.StaticUdnIpAddresses = true
		DeferCleanup(func() { settings.Settings.StaticUdnIpAddresses = previous })
		vm := staticIPTestVM(
			[]vsphere.NIC{
				{MAC: firstStaticIPMAC, Network: vsphere.Ref{ID: "net-A"}, DeviceKey: 1},
				{MAC: secondStaticIPMAC, Network: vsphere.Ref{ID: "net-B"}, DeviceKey: 2},
			},
			[]vsphere.GuestNetwork{
				{MAC: firstStaticIPMAC, DeviceConfigId: 1, IP: "10.220.0.5"},
				{MAC: secondStaticIPMAC, DeviceConfigId: 2, IP: "10.220.0.6"},
			},
		)
		builder := staticIPTestBuilder(vm, []api.NetworkPair{
			{Source: api.NetworkSourceRef{Ref: ref.Ref{ID: "net-A"}}, Destination: api.DestinationNetwork{Type: Pod}},
			{Source: api.NetworkSourceRef{Ref: ref.Ref{ID: "net-B"}}, Destination: api.DestinationNetwork{Type: Pod}},
		}, map[string]model.Network{
			"net-A": {Resource: model.Resource{ID: "net-A"}},
			"net-B": {Resource: model.Resource{ID: "net-B"}},
		}, []string{firstStaticIPMAC})
		builder.Destination.Client = staticIPUDNClient()
		spec := &cnv.VirtualMachineSpec{Template: &cnv.VirtualMachineInstanceTemplateSpec{}}

		Expect(builder.mapNetworks(&vm, spec)).To(Succeed())
		annotation := spec.Template.ObjectMeta.Annotations[planbase.AnnStaticUdnIp]
		var got map[string][]string
		Expect(json.Unmarshal([]byte(annotation), &got)).To(Succeed())
		Expect(got).To(Equal(map[string][]string{"net-1": {"10.220.0.6"}}))
	})

	It("does not require guest network data when every NIC is excluded", func() {
		vm := staticIPTestVM([]vsphere.NIC{{MAC: firstStaticIPMAC}}, nil)
		builder := staticIPTestBuilder(vm, nil, nil, []string{strings.ToUpper(firstStaticIPMAC)})
		builder.Plan.Spec.VMs[0].ID = ""
		builder.Plan.Spec.VMs[0].Name = vm.Name
		validator := &Validator{Context: builder.Context}

		ok, err := validator.StaticIPs(ref.Ref{ID: vm.ID})
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())

		builder.Plan.Spec.VMs[0].ExcludeNICsFromStaticIPs = nil
		ok, err = validator.StaticIPs(ref.Ref{ID: vm.ID})
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeFalse())
	})

	It("matches remaining NICs to guest networks across MAC formatting", func() {
		vm := staticIPTestVM(
			[]vsphere.NIC{{MAC: firstStaticIPMAC}, {MAC: secondStaticIPMAC}},
			[]vsphere.GuestNetwork{{
				MAC: strings.ToUpper(strings.ReplaceAll(firstStaticIPMAC, ":", "-")), IP: "10.0.0.1",
			}},
		)
		builder := staticIPTestBuilder(vm, nil, nil, []string{secondStaticIPMAC})
		validator := &Validator{Context: builder.Context}

		ok, err := validator.StaticIPs(ref.Ref{ID: vm.ID})
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
	})

	It("does not validate an excluded UDN NIC's IP against the destination subnet", func() {
		vm := staticIPTestVM(
			[]vsphere.NIC{{MAC: strings.ToUpper(firstStaticIPMAC)}},
			[]vsphere.GuestNetwork{{
				MAC: strings.ToUpper(firstStaticIPMAC), IP: "192.168.1.5", Network: "source-a",
			}},
		)
		builder := staticIPTestBuilder(vm, []api.NetworkPair{{
			Source:      api.NetworkSourceRef{Ref: ref.Ref{ID: "net-A"}},
			Destination: api.DestinationNetwork{Type: Pod},
		}}, map[string]model.Network{"net-A": {Resource: model.Resource{ID: "net-A", Name: "source-a"}}}, []string{firstStaticIPMAC})
		builder.Destination.Client = staticIPUDNClient()
		validator := &Validator{Context: &plancontext.Context{
			Plan:        builder.Plan,
			Source:      builder.Source,
			Destination: builder.Destination,
			Log:         builder.Log,
		}}

		ok, err := validator.UdnStaticIPs(ref.Ref{ID: vm.ID}, builder.Destination.Client)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())

		builder.Plan.Spec.VMs[0].ExcludeNICsFromStaticIPs = nil
		ok, err = validator.UdnStaticIPs(ref.Ref{ID: vm.ID}, builder.Destination.Client)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeFalse())
	})
})
