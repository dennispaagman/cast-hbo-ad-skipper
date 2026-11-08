package config

import (
	"net"
	"testing"
)

func TestLoadDevices(t *testing.T) {
	c, err := Load([]string{"-devices", "192.168.1.79, ,[::1]:8010,[::1],"})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Devices) != 3 {
		t.Fatalf("expected 3 devices, got %d", len(c.Devices))
	}
	if !c.Devices[0].AddrV4.Equal(net.ParseIP("192.168.1.79")) || c.Devices[0].Port != 8009 {
		t.Errorf("unexpected first device: %+v", c.Devices[0])
	}
	if !c.Devices[1].AddrV6.Equal(net.IPv6loopback) || c.Devices[1].Port != 8010 {
		t.Errorf("unexpected second device: %+v", c.Devices[1])
	}
	if !c.Devices[2].AddrV6.Equal(net.IPv6loopback) || c.Devices[2].Port != 8009 {
		t.Errorf("unexpected third device: %+v", c.Devices[2])
	}
}

func TestLoadRejectsEmptyDeviceList(t *testing.T) {
	if _, err := Load([]string{"-devices", " , "}); err == nil {
		t.Fatal("expected error for a device list without addresses")
	}
	if _, err := Load([]string{"-devices", ":8009"}); err == nil {
		t.Fatal("expected error for a device without host")
	}
}

func TestHealthCheckSkipsDeviceResolution(t *testing.T) {
	c, err := Load([]string{"-healthcheck", "-devices", "does-not-resolve.invalid", "-network-interface", "does-not-exist0"})
	if err != nil {
		t.Fatalf("healthcheck mode should not resolve devices or interfaces: %v", err)
	}
	if !c.HealthCheck {
		t.Fatal("HealthCheck not set")
	}
}
