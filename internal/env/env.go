package env

import (
	"fmt"
	"os"
	"sort"
)

// DisplayEnvVars prints sorted environment variables.
func DisplayEnvVars() {
	envVars := os.Environ() // Gets environment variables as "key=value" strings
	sort.Strings(envVars)   // Sorts them alphabetically
	fmt.Println("--- Environment Variables ---")
	for _, envVar := range envVars {
		fmt.Println(envVar)
	}
	fmt.Println("---------------------------")
}
