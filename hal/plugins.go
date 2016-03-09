package hal

import (
	"fmt"
	"log"
	"regexp"
	"sync"
)

// pluginRegistry contains the plugin registration data as a singleton
type pluginRegistry struct {
	plugins   []*Plugin   // registered plugins
	instances []*Instance // instances of plugins
	mut       sync.Mutex  // concurrent access
	init      sync.Once   // initialize the singleton once
}

// Plugin is a function with metadata to assist with message routing.
// Plugins are registered at startup by the main program and wired up
// to receive events when an instance is created e.g. by the pluginmgr
// plugin.
type Plugin struct {
	Name     string    // a unique name (used to launch instances)
	Func     func(Evt) // the code to execute for each matched event
	Regex    string    // the default regex match
	Multi    bool      // whether or not multiple instances are allowed
	Broker   Broker    // the broker the plugin is tied to
	Settings []Pref    // required+autoloaded preferences + defaults
	Secrets  []string  // required+autoloaded secret key names
}

// Instance is an instance of a plugin tied to a channel.
type Instance struct {
	*Plugin
	Channel  string         // channel to subscribe to
	Regex    string         // a regex for filtering messages
	Settings []Pref         // runtime settings for the instance
	regex    *regexp.Regexp // the compiled regex
}

var pluginRegSingleton pluginRegistry

func PluginRegistry() *pluginRegistry {
	pluginRegSingleton.init.Do(func() {
		pluginRegSingleton.plugins = make([]*Plugin, 0)
		pluginRegSingleton.instances = make([]*Instance, 0)
	})

	return &pluginRegSingleton
}

// Register registers a plugin with the bot.
func (p *Plugin) Register() error {
	pr := PluginRegistry()
	pr.mut.Lock()
	defer pr.mut.Unlock()

	for _, plugin := range pr.plugins {
		if plugin.Name == p.Name {
			log.Printf("Ignoring multiple calls to Register() for plugin '%s'", p.Name)
			return nil
		}
	}

	pr.plugins = append(pr.plugins, p)

	return nil
}

// Instance creates an instance of a plugin. It is *not* registered (and
// therefore not considered by the router until that is done).
func (p *Plugin) Instance(channel string) *Instance {
	i := Instance{
		Plugin:  p,
		Channel: channel,
		Regex:   p.Regex,
	}

	return &i
}

// Register an instance with the bot so that it starts receiving messages.
func (inst *Instance) Register() error {
	pr := PluginRegistry()
	pr.mut.Lock()
	defer pr.mut.Unlock()

	// restriction: only allow unique plugin.Name/inst.Channel
	ip := inst.Plugin
	name := inst.String()
	for _, i := range pr.instances {
		// disallow multiple instances for plugins that set Multi: false
		if !ip.Multi && ip.Name == i.Plugin.Name {
			log.Fatalf("BUG: plugin '%s' being registered multiple times when only one instance is allowed.", ip.Name)
		}
		if i.String() == name {
			return fmt.Errorf("'%s' is already instantiated", name)
		}
	}

	// default to the plugin's default if no RE was provided
	if inst.Regex == "" {
		inst.Regex = inst.Plugin.Regex
	}
	// TODO: the default regex still doesn't always show up

	// TODO: manually check/return the error so the bot doesn't crash
	inst.regex = regexp.MustCompile(inst.Regex)

	// once an instance is registered, the router will automatically
	// pick it up on the next message it processes
	pr.instances = append(pr.instances, inst)

	log.Printf("Registered plugin '%s' in channel '%s' with RE match '%s'", inst.Name, inst.Channel, inst.regex)

	return nil
}

// Unregister removes an instance from the list of plugin instances.
func (inst *Instance) Unregister() error {
	pr := PluginRegistry()
	pr.mut.Lock()
	defer pr.mut.Unlock()

	var idx int
	for j, i := range pr.instances {
		// TODO: verify if pointer equality is sufficient
		if i == inst {
			idx = j
			break
		}
	}

	// delete the instance from the list
	pr.instances = append(pr.instances[:idx], pr.instances[idx+1:]...)

	log.Printf("Unregistered plugin '%s' from channel '%s'", inst.Name, inst.Channel)

	return nil
}

// LoadSettingsFromPrefs loads all of the settings specified in the plugin
// Settings list into the instance's Settings list. Any current settings
// are replaced.
func (inst *Instance) LoadSettingsFromPrefs() {
	pr := PluginRegistry()
	pr.mut.Lock()
	defer pr.mut.Unlock()

	pstgs := inst.Plugin.Settings

	// wipe the previous settings
	inst.Settings = make([]Pref, len(pstgs))

	for i, ppref := range pstgs {
		ipref := ppref.Get()
		inst.Settings[i] = ipref
	}
}

// SaveSettingsToPrefs saves runtime instance preferences to the prefs
// table in the database.
func (inst *Instance) SaveSettingsToPrefs() {
	pr := PluginRegistry()
	pr.mut.Lock()
	defer pr.mut.Unlock()

	for _, ipref := range inst.Settings {
		ipref.Set()
	}
}

// PluginList returns a snapshot of the plugin list at call time.
func (pr *pluginRegistry) PluginList() []*Plugin {
	pr.mut.Lock()
	defer pr.mut.Unlock()

	out := make([]*Plugin, len(pr.plugins))
	copy(out, pr.plugins) // intentional shallow copy
	return out
}

// InstanceList returns a snapshot of the instance list at call time.
func (pr *pluginRegistry) InstanceList() []*Instance {
	pr.mut.Lock()
	defer pr.mut.Unlock()

	// this gets called in the router for every message that comes in, so it
	// might come to pass that this will perform poorly, but for now with a
	// relatively small number of instances we'll take the copy hit in exchange
	// for not having to think about concurrent access to the list
	out := make([]*Instance, len(pr.instances))
	copy(out, pr.instances) // intentional shallow copy
	return out
}

// GetPlugin returns the plugin specified by its name string.
func (pr *pluginRegistry) GetPlugin(name string) *Plugin {
	pr.mut.Lock()
	defer pr.mut.Unlock()

	for _, p := range pr.plugins {
		if p.Name == name {
			return p
		}
	}

	return nil
}

// FindInstances returns the plugin instances that match the provided
// channel and plugin name.
func (pr *pluginRegistry) FindInstances(channel, plugin string) []*Instance {
	pr.mut.Lock()
	defer pr.mut.Unlock()

	out := make([]*Instance, 0)

	for _, i := range pr.instances {
		if i.Plugin.Name == plugin && i.Channel == channel {
			out = append(out, i)
		}
	}

	return out
}

// ActivePluginList returns a list of plugins that have registered instances.
func (pr *pluginRegistry) ActivePluginList() []*Plugin {
	out := make([]*Plugin, 0)

	// create a unique list of plugins in use by instances and return that
	for _, i := range pr.InstanceList() {
		ip := i.Plugin

		seen := false
		for _, p := range out {
			if p.Name == ip.Name {
				seen = true
			}
		}

		// make sure each plugin is only inserted once
		if !seen {
			out = append(out, ip)
		}
	}

	return out
}

// InactivePluginList returns a list of plugins that do not have any registered instances.
func (pr *pluginRegistry) InactivePluginList() []*Plugin {
	out := make([]*Plugin, 0)
	inst := pr.InstanceList()

	// for each plugin, add it to the out list only if there are no instances using it
	for _, p := range pr.PluginList() {
		active := false
		for _, i := range inst {
			if p.Name == i.Plugin.Name {
				active = true
			}
		}

		if !active {
			out = append(out, p)
		}
	}

	return out
}

func (p *Plugin) String() string {
	return fmt.Sprintf("%s/%s", p.Name, "TODO: fix this")
}

func (inst *Instance) String() string {
	return fmt.Sprintf("%s/%s", inst.Name, inst.Channel)
}