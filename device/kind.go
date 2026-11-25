package device

// Kind is what this device is, for its label: "laptop" (it has a battery
// of its own) or "computer".
func Kind() string {
	if hasBattery() {
		return "laptop"
	}
	return "computer"
}
