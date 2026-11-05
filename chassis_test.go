package redfish_test

import (
	"context"
	"errors"
	"testing"

	"github.com/go-redfish/redfish"
	"github.com/go-redfish/redfish/mockbmc"
)

const chassisPath = "/redfish/v1/Chassis/1U"

func TestChassisOracleReads(t *testing.T) {
	forEachTree(t, func(t *testing.T, b *mockbmc.BMC, c *redfish.Client, _ string) {
		ctx := context.Background()
		inv, err := c.ChassisInventory(ctx)
		if err != nil || len(inv) != 1 || inv[0]["Id"] != "1U" || inv[0]["ChassisType"] == nil {
			t.Fatalf("ChassisInventory = %v, %v", inv, err)
		}
		if one, err := c.ChassisInventory(ctx, chassisPath); err != nil || len(one) != 1 {
			t.Fatalf("ChassisInventory(1U) = %v, %v", one, err)
		}
		fans, err := c.FanInventory(ctx)
		if err != nil || len(fans) != 2 || fans[0]["Name"] != "BaseBoard System Fan" || fans[0]["ReadingUnits"] != "RPM" {
			t.Fatalf("FanInventory = %v, %v", fans, err)
		}
		pw, err := c.ChassisPower(ctx)
		if err != nil || len(pw) != 1 || len(pw[0]) == 0 {
			t.Fatalf("ChassisPower = %v, %v", pw, err)
		}
		th, err := c.ChassisThermals(ctx)
		if err != nil || len(th) != 3 || th[0]["Name"] != "CPU1 Temp" || th[0]["ReadingCelsius"] == nil {
			t.Fatalf("ChassisThermals = %v, %v", th, err)
		}
		if _, ok := th[1]["ReadingCelsius"]; ok { // CPU2 has no reading
			t.Errorf("a reading appeared from nowhere: %v", th[1])
		}
		psus, err := c.PSUInventory(ctx)
		if err != nil || len(psus) == 0 {
			t.Fatalf("PSUInventory = %v, %v", psus, err)
		}
		h, err := c.ChassisHealthReports(ctx)
		if err != nil || len(h) != 1 || h[0]["chassis_uri"] != chassisPath {
			t.Fatalf("ChassisHealthReports = %v, %v", h, err)
		}
		if rep, _ := h[0]["health"].(redfish.Props); rep["Chassis"] == nil {
			t.Errorf("health = %v", h[0]["health"])
		}
		if _, err := c.HPEThermalConfig(ctx); !errors.Is(err, redfish.ErrUnsupported) {
			t.Errorf("HPEThermalConfig on a non-HPE tree: %v", err)
		}
		if _, err := c.HPEFanPercentMin(ctx); !errors.Is(err, redfish.ErrUnsupported) {
			t.Errorf("HPEFanPercentMin on a non-HPE tree: %v", err)
		}
	})
}

func TestChassisNewResources(t *testing.T) {
	forEachTree(t, func(t *testing.T, b *mockbmc.BMC, c *redfish.Client, _ string) {
		ctx := context.Background()
		sens, err := c.ChassisSensors(ctx, chassisPath)
		if err != nil || len(sens) < 10 {
			t.Fatalf("ChassisSensors = %d sensors, %v", len(sens), err)
		}
		found := false
		for _, s := range sens {
			if s["Id"] == "CPUFan1" && s["ReadingUnits"] == "%" && s["Reading"] != nil {
				found = true
			}
		}
		if !found {
			t.Errorf("no CPUFan1 sensor with a reading in %v", sens[0])
		}
		ps, err := c.ChassisPowerSubsystem(ctx, chassisPath)
		if sup, _ := ps["PowerSupplies"].([]redfish.Props); err != nil || ps["CapacityWatts"] == nil || len(sup) != 1 || sup[0]["Id"] != "Bay1" { // Bay2 is Absent
			t.Fatalf("ChassisPowerSubsystem = %v, %v", ps, err)
		}
		ts, err := c.ChassisThermalSubsystem(ctx, chassisPath)
		if fans, _ := ts["Fans"].([]redfish.Props); err != nil || len(fans) < 2 || ts["ThermalMetrics"] == nil {
			t.Fatalf("ChassisThermalSubsystem = %v, %v", ts, err)
		}
		em, err := c.ChassisEnvironmentMetrics(ctx, chassisPath)
		if err != nil || em["PowerWatts"] == nil {
			t.Fatalf("ChassisEnvironmentMetrics = %v, %v", em, err)
		}
		if _, err := c.ChassisSensors(ctx, "/redfish/v1/Chassis/nope"); err == nil {
			t.Error("sensors of a chassis that does not exist")
		}
	})
}
