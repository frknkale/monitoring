package servicechecks

import (
	"fmt"
	"os/exec"
	"strings"

	"monitoring/logger"
	"monitoring/types"
)


func CheckServices(cfg types.Config) []map[string]interface{} {
	var services []map[string]interface{}

	if !cfg.Services.Enabled {
		return services
	}

	manager := cfg.Services.Manager
	for _, serviceName := range cfg.Services.ServiceList {
		if serviceName == "all" {
			result, err := CheckAllServices(manager)
			if err != nil {
				logger.Log.Printf("Failed to fetch all services: %v", err)
				return services
			}
			services = append(services, result...)
		} else {
			service, err := CheckServiceStatus(serviceName, manager)
			if err != nil {
				logger.Log.Printf("Failed to fetch status for %s: %v", serviceName, err)
				return services
			}
			services = append(services, service)
		}
	}
	if len(cfg.Services.ServiceList)==0 {
		result, err := CheckAllServices(manager)
		if err != nil {
			logger.Log.Printf("Failed to fetch all services: %v", err)
		} else {
			services = append(services, result...)
		}
	}
	services = applyFilters(services, cfg.Services.Filter, manager)

	return services
}

// CheckServiceStatus checks the status of a single service
func CheckServiceStatus(service, manager string) (map[string]interface{}, error) {
	var cmd *exec.Cmd
	if manager != "systemctl" && manager != "supervisorctl" {
		return nil, fmt.Errorf("unsupported manager: %s", manager)
	}

	cmd = exec.Command(manager, "status", service)
	out, err := cmd.CombinedOutput()
	output := string(out)

	var result = map[string]interface{}{"service": service}

	if err != nil {
		if strings.Contains(output, "not-found") || strings.Contains(output, "could not be found") || strings.Contains(output, "no such process"){
			result["error"] = "service not found"
			return result, nil
		} else {
			result["error"] = err.Error()
		}
	}

	switch manager {
	case "systemctl":
		result = parseSystemctl(output, result)
		enabledStatus, err := CheckServiceEnabled(service)
		if err == nil {
			result["enable_state"] = enabledStatus
		}
	case "supervisorctl":
		result = parseSupervisorctl(output, result)
	}

	return result, nil
}


func parseSystemctl(output string, result map[string]interface{}) map[string]interface{} {
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "Active:") {
			result["raw_active"] = trimmed
			parts := strings.Fields(trimmed)
			if len(parts) >= 2 {
				result["status"] = parts[1]
			}
			if left := strings.Index(trimmed, "("); left != -1 {
				right := strings.Index(trimmed, ")")
				if left != -1 && right != -1 && right > left {
					result["sub_status"] = trimmed[left+1 : right]
				}
			}
			if idx := strings.Index(trimmed, "since"); idx != -1 {
				sincePart := trimmed[idx+6:]
				if parts := strings.SplitN(sincePart, ";", 2); len(parts) == 2 {
					result["since"] = strings.TrimSpace(parts[0])
					result["duration"] = strings.TrimSpace(parts[1])
				}
			}

			result["active"] = result["status"] == "active"
		}

		if strings.HasPrefix(trimmed, "Main PID:") {
			result["pid"] = strings.TrimSpace(strings.TrimPrefix(trimmed, "Main PID:"))
		}
		if strings.HasPrefix(trimmed, "Memory:") {
			result["memory"] = strings.TrimSpace(strings.TrimPrefix(trimmed, "Memory:"))
		}
		if strings.HasPrefix(trimmed, "CPU:") {
			result["cpu"] = strings.TrimSpace(strings.TrimPrefix(trimmed, "CPU:"))
		}
	}

	return result
}


func parseSupervisorctl(output string, result map[string]interface{}) map[string]interface{} {
	parts := strings.Fields(output)
	if len(parts) >= 2 {
		result["status"] = parts[1]
	}
	result["active"] = result["status"] == "RUNNING"

	if result["active"].(bool) && len(parts) >= 4 {
		result["uptime"] = strings.Join(parts[5:], " ")
		result["pid"] = strings.Trim(parts[3],",")

	} else if result["status"] == "STOPPED" && len(parts) >= 3{
		result["stopped_at"] = strings.Join(parts[2:], " ")
	}

	return result
}


func CheckAllServices(manager string) ([]map[string]interface{}, error) {
	var cmd *exec.Cmd
	switch manager {
	case "systemctl":
		cmd = exec.Command("systemctl", "list-units", "--type=service", "--no-pager", "--no-legend")
	case "supervisorctl":
		cmd = exec.Command("supervisorctl", "status")
	default:
		return nil, fmt.Errorf("unsupported manager: %s", manager)
	}

	out, err := cmd.CombinedOutput()
	if err != nil && !strings.Contains(err.Error(), "exit status 3"){  // not really an error, CheckServiceStatus will handle
		logger.Log.Printf("Error running %s list: %v", manager, err)
		fmt.Printf("Error running %s list: %v\n", manager, err)
		return nil, err
	}

	return parseServiceList(string(out), manager), nil
}


func parseServiceList(output string, manager string) []map[string]interface{} {
	var services []map[string]interface{}
	lines := strings.Split(output, "\n")

	for _, line := range lines {
		parts := strings.Fields(strings.ReplaceAll(line, "●", ""))
		if len(parts) < 2 {
			continue
		}

		// systemctl: nginx.service
		// supervisorctl: just "nginx"
		serviceName := parts[0]
		if manager == "systemctl" && !strings.HasSuffix(serviceName, ".service") {
			continue
		}

		service, err := CheckServiceStatus(serviceName, manager)
		if err != nil {
			continue
		}

		services = append(services, service)
	}

	return services
}

func CheckServiceEnabled(service string) (string, error) {
    cmd := exec.Command("systemctl", "is-enabled", service)
    out, err := cmd.CombinedOutput()
    enabled := strings.TrimSpace(string(out))
    if err != nil && enabled == "" {
        return "unknown", err
    }
    return enabled, nil
}

func applyFilters(services []map[string]interface{}, filter types.ServiceFilter, manager string) []map[string]interface{} {
	var filtered []map[string]interface{}

	for _, service := range services{
		if filter.State != "" {
			if status, ok := service["status"].(string); !ok || status != filter.State {
				continue
			}
		}
		if manager == "systemctl" {
			if filter.SubState != "" {
				if subStatus, ok := service["sub_status"].(string); !ok || subStatus != filter.SubState {
					continue
				}
			}
			if filter.EnableState != "" {
				if enableState, ok := service["enable_state"].(string); !ok || enableState != filter.EnableState {
					continue
				}
			}
		}
		filtered = append(filtered, service)
	}
    return filtered

}
