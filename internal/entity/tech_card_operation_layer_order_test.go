package entity

import "testing"

func TestTechCardLayerOrderColumn(t *testing.T) {
	if v, err := TechCardLayerOrder(nil).Value(); err != nil || v != nil {
		t.Fatalf("empty is NULL, got %v, %v", v, err)
	}
	v, err := TechCardLayerOrder{"BACK", "POCKET"}.Value()
	if err != nil || v != `["BACK","POCKET"]` {
		t.Fatalf("got %v, %v", v, err)
	}
	var o TechCardLayerOrder
	if err := o.Scan([]byte(`["BACK","POCKET"]`)); err != nil || len(o) != 2 || o[1] != "POCKET" {
		t.Fatalf("got %v, %v", o, err)
	}
	if err := o.Scan(nil); err != nil || o != nil {
		t.Fatalf("NULL clears, got %v, %v", o, err)
	}
	if err := o.Scan([]byte(`[]`)); err != nil || o != nil {
		t.Fatalf("an empty array reads as no order, got %v, %v", o, err)
	}
	if err := o.Scan([]byte(`not json`)); err == nil {
		t.Fatal("a broken value is an error")
	}
}
