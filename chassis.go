package redfish

import (
	"context"
	"fmt"
)

// The Chassis functions are a port of the chassis half of community.general's
// redfish_utils.py at 33e521d6 (get_chassis_inventory, get_fan_inventory,
// get_chassis_power, get_chassis_thermals, get_psu_inventory,
// get_chassis_health_report, get_hpe_thermal_config, get_hpe_fan_percent_min),
// which read the deprecated Power and Thermal resources, plus the resources
// that replace them (DSP8010 Chassis 1.29 and the schemas the mock trees serve:
// Sensor 1.8.1, PowerSubsystem and ThermalSubsystem 1.0.0, EnvironmentMetrics).
//
// A function that takes uris reads those chassis; with none given it reads every
// chassis of the service, as the oracle does.

// chassisSet is uris, or every chassis of the service when none is given.
func (c *Client) chassisSet(ctx context.Context, uris []string) ([]string, error) {
	if len(uris) > 0 {
		return uris, nil
	}
	return c.ChassisURIs(ctx)
}

// ChassisInventory lists the identity properties of the chassis
// (get_chassis_inventory).
func (c *Client) ChassisInventory(ctx context.Context, uris ...string) ([]Props, error) {
	if len(uris) == 0 { // every chassis: the collection's members, expanded when the service allows
		root, err := c.Root(ctx)
		if err != nil {
			return nil, err
		}
		if root.Chassis == "" {
			return nil, fmt.Errorf("%w: the service root links no Chassis collection", ErrNotFound)
		}
		members, err := c.memberResources(ctx, root.Chassis, MaxMembers, 8, nil)
		if err != nil {
			return nil, err
		}
		out := []Props{}
		for _, m := range members {
			out = append(out, pick(m.Props, chassisInventoryProperties...))
		}
		return out, nil
	}
	set, err := c.chassisSet(ctx, uris)
	if err != nil {
		return nil, err
	}
	out := []Props{}
	for _, u := range set {
		p, _, err := c.getProps(ctx, u)
		if err != nil {
			return nil, err
		}
		out = append(out, pick(p, chassisInventoryProperties...))
	}
	return out, nil
}

var chassisInventoryProperties = []string{"Name", "Id", "ChassisType", "PartNumber", "AssetTag", "Manufacturer", "IndicatorLED", "SerialNumber", "Model"}

// legacy reads the resource a chassis links under key ("Thermal", "Power"):
// nil when the chassis has no such link.
func (c *Client) legacy(ctx context.Context, chassis, key string) (Props, error) {
	ch, _, err := c.getProps(ctx, chassis)
	if err != nil {
		return nil, err
	}
	u := linkOf(ch, key)
	if u == "" {
		return nil, nil
	}
	p, _, err := c.getProps(ctx, u)
	return p, err
}

// FanInventory lists the fans of the Thermal resources (get_fan_inventory); a
// Thermal resource without Fans is an error ("No Fans present").
func (c *Client) FanInventory(ctx context.Context, uris ...string) ([]Props, error) {
	set, err := c.chassisSet(ctx, uris)
	if err != nil {
		return nil, err
	}
	out := []Props{}
	for _, u := range set {
		th, err := c.legacy(ctx, u, "Thermal")
		if err != nil {
			return nil, err
		}
		if th == nil {
			continue
		}
		fans, ok := th["Fans"].([]any)
		if !ok {
			return nil, fmt.Errorf("%w: No Fans present", ErrNotFound)
		}
		for _, x := range fans {
			f, _ := x.(map[string]any)
			out = append(out, pick(f, "Name", "FanName", "Reading", "ReadingUnits", "Status"))
		}
	}
	return out, nil
}

// ChassisPower is the first PowerControl entry of each chassis' Power resource
// (get_chassis_power); an error when no chassis has a Power resource.
func (c *Client) ChassisPower(ctx context.Context, uris ...string) ([]Props, error) {
	set, err := c.chassisSet(ctx, uris)
	if err != nil {
		return nil, err
	}
	out := []Props{}
	for _, u := range set {
		pw, err := c.legacy(ctx, u, "Power")
		if err != nil {
			return nil, err
		}
		if pw == nil {
			continue
		}
		entry := Props{}
		if pc, _ := pw["PowerControl"].([]any); len(pc) > 0 {
			first, _ := pc[0].(map[string]any)
			entry = pick(first, "Name", "PowerAllocatedWatts", "PowerAvailableWatts", "PowerCapacityWatts", "PowerConsumedWatts", "PowerMetrics", "PowerRequestedWatts", "RelatedItem", "Status")
		}
		out = append(out, entry)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: Power information not found", ErrNotFound)
	}
	return out, nil
}

// ChassisThermals lists the Temperatures of the Thermal resources
// (get_chassis_thermals); a property that is null is left out.
func (c *Client) ChassisThermals(ctx context.Context, uris ...string) ([]Props, error) {
	set, err := c.chassisSet(ctx, uris)
	if err != nil {
		return nil, err
	}
	out := []Props{}
	for _, u := range set {
		th, err := c.legacy(ctx, u, "Thermal")
		if err != nil {
			return nil, err
		}
		temps, _ := th["Temperatures"].([]any)
		for _, x := range temps {
			t, _ := x.(map[string]any)
			e := pick(t, "Name", "PhysicalContext", "UpperThresholdCritical", "UpperThresholdFatal", "UpperThresholdNonCritical", "LowerThresholdCritical", "LowerThresholdFatal", "LowerThresholdNonCritical", "MaxReadingRangeTemp", "MinReadingRangeTemp", "ReadingCelsius", "RelatedItem", "SensorNumber", "Status")
			for k, v := range e {
				if v == nil {
					delete(e, k)
				}
			}
			out = append(out, e)
		}
	}
	return out, nil
}

// ChassisHealthReport is the health of a chassis and of its power supplies, fans
// and PCIe devices (get_chassis_health_report).
func (c *Client) ChassisHealthReport(ctx context.Context, chassisURI string) (Props, error) {
	return c.HealthReport(ctx, "Chassis", chassisURI, []string{"Power.PowerSupplies", "Thermal.Fans", "Links.PCIeDevices"})
}

// ChassisHealthReports is ChassisHealthReport of each chassis, each entry naming
// its chassis_uri (get_multi_chassis_health_report).
func (c *Client) ChassisHealthReports(ctx context.Context, uris ...string) ([]Props, error) {
	set, err := c.chassisSet(ctx, uris)
	if err != nil {
		return nil, err
	}
	out := []Props{}
	for _, u := range set {
		h, err := c.ChassisHealthReport(ctx, u)
		if err != nil {
			return nil, err
		}
		out = append(out, Props{"chassis_uri": u, "health": h})
	}
	return out, nil
}

// oemHpe reads a property of Oem.Hpe on the first chassis that has it; ok is
// false when none does.
func (c *Client) oemHpe(ctx context.Context, key string, uris []string) (any, bool, error) {
	set, err := c.chassisSet(ctx, uris)
	if err != nil {
		return nil, false, err
	}
	for _, u := range set {
		p, _, err := c.getProps(ctx, u)
		if err != nil {
			return nil, false, err
		}
		if v := sub(sub(p, "Oem"), "Hpe")[key]; v != nil {
			return v, true, nil
		}
	}
	return nil, false, nil
}

// HPEThermalConfig is Oem.Hpe.ThermalConfiguration of the first chassis that has
// it (get_hpe_thermal_config); ErrUnsupported when none does.
func (c *Client) HPEThermalConfig(ctx context.Context, uris ...string) (any, error) {
	v, ok, err := c.oemHpe(ctx, "ThermalConfiguration", uris)
	if err == nil && !ok {
		err = fmt.Errorf("%w: Oem.Hpe.ThermalConfiguration", ErrUnsupported)
	}
	return v, err
}

// HPEFanPercentMin is Oem.Hpe.FanPercentMinimum of the first chassis that has it
// (get_hpe_fan_percent_min); ErrUnsupported when none does.
func (c *Client) HPEFanPercentMin(ctx context.Context, uris ...string) (any, error) {
	v, ok, err := c.oemHpe(ctx, "FanPercentMinimum", uris)
	if err == nil && !ok {
		err = fmt.Errorf("%w: Oem.Hpe.FanPercentMinimum", ErrUnsupported)
	}
	return v, err
}

// ChassisSensors lists the Sensor resources of a chassis (its Sensors
// collection): what each reads, in which unit and range, and its Status.
func (c *Client) ChassisSensors(ctx context.Context, chassisURI string) ([]Props, error) {
	return c.memberProps(ctx, chassisURI, "Sensors", []string{"Id", "Name", "ReadingType", "Reading", "ReadingUnits", "ReadingRangeMin", "ReadingRangeMax", "PhysicalContext", "Status"}, nil, false)
}

// ChassisPowerSubsystem is the PowerSubsystem of a chassis (the capacity, the
// allocation, the Status) and its power supplies; ErrUnsupported when the chassis
// has none.
func (c *Client) ChassisPowerSubsystem(ctx context.Context, chassisURI string) (Props, error) {
	ch, _, err := c.getProps(ctx, chassisURI)
	if err != nil {
		return nil, err
	}
	u := linkOf(ch, "PowerSubsystem")
	if u == "" {
		return nil, fmt.Errorf("%w: PowerSubsystem", ErrUnsupported)
	}
	ps, _, err := c.getProps(ctx, u)
	if err != nil {
		return nil, err
	}
	out := pick(ps, "Name", "CapacityWatts", "Allocation", "PowerSupplyRedundancy", "Status")
	supplies, err := c.memberProps(ctx, u, "PowerSupplies", []string{"Id", "Name", "Model", "SerialNumber", "PartNumber", "Manufacturer", "FirmwareVersion", "PowerCapacityWatts", "PowerSupplyType", "Status"}, func(m Props) bool { return sub(m, "Status")["State"] != "Absent" }, false)
	if err != nil {
		return nil, err
	}
	out["PowerSupplies"] = supplies
	return out, nil
}

// ChassisThermalSubsystem is the ThermalSubsystem of a chassis (its Status and
// fan redundancy), its fans and its ThermalMetrics; ErrUnsupported when the
// chassis has none.
func (c *Client) ChassisThermalSubsystem(ctx context.Context, chassisURI string) (Props, error) {
	ch, _, err := c.getProps(ctx, chassisURI)
	if err != nil {
		return nil, err
	}
	u := linkOf(ch, "ThermalSubsystem")
	if u == "" {
		return nil, fmt.Errorf("%w: ThermalSubsystem", ErrUnsupported)
	}
	ts, _, err := c.getProps(ctx, u)
	if err != nil {
		return nil, err
	}
	out := pick(ts, "Name", "FanRedundancy", "Status")
	if _, ok := ts["Fans"]; ok {
		fans, err := c.memberProps(ctx, u, "Fans", []string{"Id", "Name", "SpeedPercent", "PhysicalContext", "Manufacturer", "Model", "Status"}, func(m Props) bool { return sub(m, "Status")["State"] != "Absent" }, false)
		if err != nil {
			return nil, err
		}
		out["Fans"] = fans
	}
	if mu := linkOf(ts, "ThermalMetrics"); mu != "" {
		m, _, err := c.getProps(ctx, mu)
		if err != nil {
			return nil, err
		}
		out["ThermalMetrics"] = pick(m, "TemperatureReadingsCelsius", "TemperatureSummaryCelsius", "HeatingCoolingEfficiencyPercent")
	}
	return out, nil
}

// ChassisEnvironmentMetrics is the EnvironmentMetrics of a chassis: its power,
// energy, temperature, humidity and fan speeds, as the service reports them;
// ErrUnsupported when the chassis has none.
func (c *Client) ChassisEnvironmentMetrics(ctx context.Context, chassisURI string) (Props, error) {
	ch, _, err := c.getProps(ctx, chassisURI)
	if err != nil {
		return nil, err
	}
	u := linkOf(ch, "EnvironmentMetrics")
	if u == "" {
		return nil, fmt.Errorf("%w: EnvironmentMetrics", ErrUnsupported)
	}
	m, _, err := c.getProps(ctx, u)
	if err != nil {
		return nil, err
	}
	return pick(m, "Name", "PowerWatts", "EnergykWh", "EnergyJoules", "TemperatureCelsius", "HumidityPercent", "FanSpeedsPercent", "PowerLimitWatts"), nil
}
