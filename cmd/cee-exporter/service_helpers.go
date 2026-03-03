package main

// parseCfgPath extracts the value of -config or --config from args
// without calling flag.Parse(). Returns "config.toml" if not found.
// Used by runWithServiceManager on Windows to store the config path in
// the SCM registry at install time.
func parseCfgPath(args []string) string {
	for i, a := range args {
		if (a == "-config" || a == "--config") && i+1 < len(args) {
			return args[i+1]
		}
	}
	return "config.toml"
}
