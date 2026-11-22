package bus

// SetTestBeforeSet sets testBeforeSet for the package's external tests.
func SetTestBeforeSet(f func(refs []string)) { testBeforeSet = f }
