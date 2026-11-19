package seed

// Class describes what a migration-created row means to a newly provisioned team.
type Class uint8

const (
	Keep Class = iota + 1
	Scrub
	Derived
)

// classes is built per call so no package-level state exists (tenantgate G4).
func classes() map[string]Class {
	return map[string]Class{
		// Keep: shared dictionaries, fixed catalogs, statuses, and required neutral settings.
		"acct_account":                 Keep,
		"admin_specialty":              Keep,
		"ai_provider":                  Keep,
		"ai_route":                     Keep,
		"ai_settings":                  Keep,
		"alert_setting":                Keep,
		"announce":                     Keep,
		"announce_translation":         Keep,
		"care_symbol":                  Keep,
		"care_symbol_translation":      Keep,
		"category":                     Keep,
		"category_level":               Keep,
		"category_size_system":         Keep,
		"category_translation":         Keep,
		"color":                        Keep,
		"country":                      Keep,
		"design_settings":              Keep,
		"dictionary_revision":          Keep,
		"fiber":                        Keep,
		"fiber_label_translation":      Keep,
		"hero_background_color":        Keep,
		"language":                     Keep,
		"measurement_name":             Keep,
		"measurement_name_translation": Keep,
		"operation_work":               Keep,
		"operation_work_machine":       Keep,
		"operation_work_syn":           Keep,
		"order_status":                 Keep,
		"payment_method":               Keep,
		"shipment_carrier":             Keep,
		"size":                         Keep,
		"task_board":                   Keep,
		"tech_card":                    Keep,
		"tech_card_assembly_clock":     Keep,
		"tier_config":                  Keep,
		"vat_rate":                     Keep,
		"workshop_settings":            Keep,

		// Scrub: grbpwr shop pricing and the first tenant's file-library taxonomy.
		"complimentary_shipping_price": Scrub,
		"file_role":                    Scrub,
		"file_topic":                   Scrub,
		"shipment_carrier_price":       Scrub,

		// Derived: migration backfills and conflict reports have no independent seed data.
		"archive_main_media":                    Derived,
		"archive_translation":                   Derived,
		"collection":                            Derived,
		"fitting_change_request_piece":          Derived,
		"material_fabric_attr":                  Derived,
		"migration_0139_style_field_conflict":   Derived,
		"migration_0139_style_field_guard":      Derived,
		"migration_0141_chart_conflict":         Derived,
		"migration_0141_chart_guard":            Derived,
		"migration_0141_chart_provenance":       Derived,
		"migration_0148_archive_code_conflict":  Derived,
		"migration_0148_archive_code_guard":     Derived,
		"migration_0151_merge_conflict":         Derived,
		"migration_0151_merge_guard":            Derived,
		"migration_0152_orphan_conflict":        Derived,
		"migration_0152_orphan_guard":           Derived,
		"migration_0154_country_alias":          Derived,
		"migration_0154_country_guard":          Derived,
		"migration_0154_country_quarantine":     Derived,
		"migration_colorway_merge_map":          Derived,
		"model_default_size":                    Derived,
		"opex_line":                             Derived,
		"order_status_history":                  Derived,
		"packaging_recipe":                      Derived,
		"product":                               Derived,
		"product_lab_dip_round":                 Derived,
		"product_price":                         Derived,
		"product_translation":                   Derived,
		"production_run_line":                   Derived,
		"production_run_receipt":                Derived,
		"production_run_receipt_line":           Derived,
		"style_model_no_allocation":             Derived,
		"tag":                                   Derived,
		"task_assignee":                         Derived,
		"task_status_history":                   Derived,
		"tech_card_bom_item":                    Derived,
		"tech_card_colorway_usage":              Derived,
		"tech_card_costing_migration_exception": Derived,
		"tech_card_equipment_profile":           Derived,
		"tech_card_marker_size":                 Derived,
		"tech_card_operation_bom":               Derived,
		"tech_card_operation_input":             Derived,
		"tech_card_packaging_item":              Derived,
		"tech_card_packaging_item_colorway":     Derived,
		"tech_card_role_assignment":             Derived,
		"tech_card_role_backfill_report":        Derived,
		"tech_card_size_chart_point":            Derived,
		"tech_card_size_measurement":            Derived,
		"tech_card_style_number_dedup_report":   Derived,
	}
}

// scrubUpdates lists required Keep tables whose seeded tenant text is neutralized.
func scrubUpdates() map[string]struct{} {
	return map[string]struct{}{
		"design_settings": {},
		"tier_config":     {},
	}
}

// Classes returns the seed classification (a fresh map on every call).
func Classes() map[string]Class {
	return classes()
}
