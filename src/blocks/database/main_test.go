package main

import (
	"reflect"
	"sort"
	"testing"
)

// Two transactions over the same keys take the same locks in the same
// order whatever order they name the keys in.
func TestTxnStripesAreOrdered(t *testing.T) {
	a := txnStripes([]op{{Key: "order/1"}, {Key: "stock/7"}, {Key: "payment/3"}, {Key: "order/1"}})
	b := txnStripes([]op{{Key: "payment/3"}, {Key: "order/1"}, {Key: "stock/7"}})
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("%v and %v", a, b)
	}
	if !sort.IntsAreSorted(a) || len(a) == 0 || len(a) > 3 {
		t.Fatalf("stripes %v", a)
	}
	for _, i := range a {
		if i < 0 || i >= stripes {
			t.Fatalf("stripe %d out of range", i)
		}
	}
}
