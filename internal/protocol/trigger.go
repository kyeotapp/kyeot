package protocol

// Run a routine now. Optional: the server sends it only if the agent listed FeatureRunRoutine in hello's features.
// Agents that don't know this type can ignore it. The result comes back as usual via routine.run.
const (
	FeatureRunRoutine  = "routines.run"
	TypeRoutineTrigger = "routine.trigger" // server → agent; Routine holds the routine id
)

// Understands routine.create, routine.toggle, routine.remove. Declared via hello; without it the server doesn't send those commands.
const FeatureManageRoutines = "routines.manage"

// Refresh a bundle now. Optional: the server sends it only if the agent listed FeatureDataRefresh in hello's features.
// Used by the kyeot bridge's sensors (network). The agent recomputes the bundle named in Name and uploads it with data.put. Ignore if unknown.
const (
	FeatureDataRefresh   = "data.refresh"
	TypeDataRefresh      = "data.refresh"   // server → agent; Name holds the bundle name
	FeatureSensorNetwork = "sensor.network" // the network sensor on this agent's computer uploads the netwatch bundle
	FeatureSensorDisk    = "sensor.disk"    // the disk sensor uploads the disk bundle
	FeatureSensorSystem  = "sensor.system"  // the system sensor uploads the system bundle (CPU, memory, battery)
)
