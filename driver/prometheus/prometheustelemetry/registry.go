package prometheustelemetry

import (
	"errors"
	"fmt"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/driver/prometheus/prometheustelemetry/internal/collector"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// registerBuiltins registers the runtime collectors and target_info.
func (driver *Driver) registerBuiltins(resource telemetry.Resource) error {
	if driver.config.RuntimeMetrics {
		if err := driver.registerRuntimeMetrics(); err != nil {
			return err
		}
	}

	if !driver.config.TargetInfo {
		return nil
	}

	info, err := collector.NewTargetInfo(resource)

	if err != nil {
		return fmt.Errorf("build target_info: %w", err)
	}

	if info == nil {
		return nil
	}

	if err := driver.register(info); err != nil {
		return fmt.Errorf("register target_info: %w", err)
	}

	return nil
}

// register adds one owned collector and remembers its release only on success.
// Conflicts remain errors; accepting a preexisting runtime collector is the
// separate ownership policy in registerRuntimeMetrics.
func (driver *Driver) register(collector prometheus.Collector) error {
	if err := driver.config.Registerer.Register(collector); err != nil {
		return err
	}

	driver.owned = append(driver.owned, collector)

	return nil
}

// registerRuntimeMetrics borrows equivalent preexisting collectors without
// taking ownership. Newly registered collectors join the same rollback and Stop
// stack as application instruments; native registration failures are returned.
func (driver *Driver) registerRuntimeMetrics() error {
	runtime := []prometheus.Collector{collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{})}

	for _, collector := range runtime {
		err := driver.register(collector)
		var already prometheus.AlreadyRegisteredError

		if errors.As(err, &already) {
			continue
		}

		if err != nil {
			return fmt.Errorf("register runtime metrics: %w", err)
		}
	}

	return nil
}

// unregisterOwned removes every collector the driver registered.
func (driver *Driver) unregisterOwned() {
	for _, collector := range driver.owned {
		driver.config.Registerer.Unregister(collector)
	}

	driver.owned = nil
}
