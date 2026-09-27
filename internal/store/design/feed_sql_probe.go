package design

// CardOutputsFeedSQL — the SQL text of the card-outputs feed (GetBand's `outputs` and its counts),
// exported ONLY so that probes OUTSIDE internal/store can read and execute it.
//
// ⚠ WHY THIS EXISTS AT ALL. Tests under internal/store are compile-only on a developer machine:
// the parent package's TestMain migrates and then DROPS every table of whatever DSN the config
// names, and that DSN has been production before. So the twin of entity.DesignWorkflowOf and the
// «one expression for stamp, window and count» guard must be RUNNABLE somewhere else — they live in
// apisrv/admin/design_feed_workflow_twin_test.go and read the text from here. Nothing in production
// code calls this; it hands out copies of constants and cannot change what the store runs.
type CardOutputsFeedSQL struct {
	// Scope is FROM + WHERE (the whole-card predicate), shared by List and Count.
	Scope string
	// Colorway and Section are the first two window keys (designCardOutputsColorway/Section).
	Colorway string
	Section  string
	// Workflow is designCardOutputsWorkflow — the run_workflow stamp, the twin of
	// entity.DesignWorkflowOf. It references `r.kind` and `r.params` and nothing else.
	Workflow string
	// FabricPicture is the recolor branch's «some cloth has media_id > 0» predicate (inside Workflow).
	FabricPicture string
	// WindowKey is the third window key: Workflow inside section 1, '' elsewhere.
	WindowKey string
	// List and Count are the two statements GetBand actually executes (named params :card,
	// :per_colorway).
	List  string
	Count string
}

// CardOutputsFeed returns the feed SQL exactly as the store runs it.
func CardOutputsFeed() CardOutputsFeedSQL {
	return CardOutputsFeedSQL{
		Scope:         designCardOutputsFrom + designCardOutputsWhere,
		Colorway:      designCardOutputsColorway,
		Section:       designCardOutputsSection,
		Workflow:      designCardOutputsWorkflow,
		FabricPicture: designCardOutputsFabricPicture,
		WindowKey:     designCardOutputsWindowKey(designCardOutputsSection, designCardOutputsWorkflow),
		List:          designListCardOutputs,
		Count:         designCountCardOutputsByColorway,
	}
}

// CardOutputsFeedStatements is the statements builder itself, for the sentinel probe: called with
// markers instead of the real pieces, it must carry each marker exactly where it is used and no
// word of the real predicate or of the real workflow CASE.
func CardOutputsFeedStatements(scope, colorway, section, workflow string) (list, count string) {
	return designCardOutputsStatements(scope, colorway, section, workflow)
}

// CardOutputsWindowKey is designCardOutputsWindowKey, for the same probe.
func CardOutputsWindowKey(section, workflow string) string {
	return designCardOutputsWindowKey(section, workflow)
}
