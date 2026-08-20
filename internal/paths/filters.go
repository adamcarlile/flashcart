package paths

import "fmt"

// ignoredRules exclude the Synology indexer and rsync's partial directory at
// any depth. rsync applies rules in order and the first match wins, so these
// must lead every rule set.
func ignoredRules() []string {
	return []string{
		fmt.Sprintf("- %s/", ignoredComponent),
		fmt.Sprintf("- %s/", PartialDir),
	}
}

// PlainFilters are for trees with no content/metadata split, namely bios and
// saves. They exclude only the never-transferred paths.
func PlainFilters() []string {
	return ignoredRules()
}

// ContentFilters select NAS-owned ROM content by excluding the box-owned
// metadata. The leading slash anchors each pattern to the transfer root, so
// "/*/images/" matches "snes/images" but never
// "ps3/God of War Collection.ps3/images".
func ContentFilters() []string {
	rules := ignoredRules()
	rules = append(rules, fmt.Sprintf("- /*/%s", MetadataFile))
	for _, d := range MetadataDirs {
		rules = append(rules, fmt.Sprintf("- /*/%s/", d))
	}
	return rules
}

// MetadataFilters select box-owned metadata and nothing else. System
// directories are included so rsync descends into them; everything not
// explicitly included is then excluded by the trailing catch-all.
func MetadataFilters() []string {
	rules := ignoredRules()
	rules = append(rules, "+ /*/")
	rules = append(rules, fmt.Sprintf("+ /*/%s", MetadataFile))
	for _, d := range MetadataDirs {
		rules = append(rules, fmt.Sprintf("+ /*/%s/", d))
		rules = append(rules, fmt.Sprintf("+ /*/%s/**", d))
	}
	rules = append(rules, "- *")
	return rules
}
