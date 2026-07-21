package extractor_test

import (
	"testing"

	"github.com/mattiasthalen/qlik-parser/internal/extractor"
)

func findTable(tables []extractor.ScriptTable, name string) *extractor.ScriptTable {
	for i := range tables {
		if tables[i].Name == name {
			return &tables[i]
		}
	}
	return nil
}

func TestParseScriptLineage_ConnectAndQVD(t *testing.T) {
	script := `
LIB CONNECT TO 'MyDataConnection';

Sales:
LOAD
    OrderID,
    Amount as SalesAmount,
    CustomerID
FROM [lib://DataFiles/sales.qvd] (qvd)
WHERE Amount > 0;
`
	lin := extractor.ParseScriptLineage(script)

	if len(lin.Connections) != 1 {
		t.Fatalf("expected 1 connection, got %d: %+v", len(lin.Connections), lin.Connections)
	}
	if lin.Connections[0].Name != "MyDataConnection" || lin.Connections[0].Kind != "LIB" {
		t.Errorf("unexpected connection: %+v", lin.Connections[0])
	}

	sales := findTable(lin.Tables, "Sales")
	if sales == nil {
		t.Fatalf("Sales table not found: %+v", lin.Tables)
	}
	if sales.SourceType != "qvd" {
		t.Errorf("expected qvd source, got %q", sales.SourceType)
	}
	if sales.Operation != "load" {
		t.Errorf("expected load operation, got %q", sales.Operation)
	}
	want := []string{"OrderID", "SalesAmount", "CustomerID"}
	if len(sales.Fields) != len(want) {
		t.Fatalf("fields = %v, want %v", sales.Fields, want)
	}
	for i, w := range want {
		if sales.Fields[i] != w {
			t.Errorf("field[%d] = %q, want %q", i, sales.Fields[i], w)
		}
	}
}

func TestParseScriptLineage_ResidentAndJoin(t *testing.T) {
	script := `
Base:
LOAD * INLINE [
a, b
1, 2
];

Summary:
NoConcatenate
LOAD a RESIDENT Base;

LEFT JOIN (Summary)
LOAD a, b RESIDENT Base;
`
	lin := extractor.ParseScriptLineage(script)

	summary := findTable(lin.Tables, "Summary")
	if summary == nil {
		t.Fatalf("Summary not found: %+v", lin.Tables)
	}
	if summary.SourceType != "resident" || summary.Source != "Base" {
		t.Errorf("unexpected Summary source: %+v", summary)
	}

	// The LEFT JOIN targets Summary.
	var join *extractor.ScriptTable
	for i := range lin.Tables {
		if lin.Tables[i].Operation == "left join" {
			join = &lin.Tables[i]
		}
	}
	if join == nil {
		t.Fatalf("left join statement not found: %+v", lin.Tables)
	}
	if join.Name != "Summary" {
		t.Errorf("left join target = %q, want Summary", join.Name)
	}
	if join.SourceType != "resident" || join.Source != "Base" {
		t.Errorf("unexpected join source: %+v", join)
	}
}

func TestParseScriptLineage_Inline(t *testing.T) {
	script := `
Cal:
LOAD * INLINE [
Month, Days
Jan, 31
];
`
	lin := extractor.ParseScriptLineage(script)
	cal := findTable(lin.Tables, "Cal")
	if cal == nil {
		t.Fatalf("Cal not found: %+v", lin.Tables)
	}
	if cal.SourceType != "inline" {
		t.Errorf("expected inline, got %q", cal.SourceType)
	}
}

func TestParseScriptLineage_SQLSelect(t *testing.T) {
	script := `
ODBC CONNECT TO [MyDB];

Orders:
SQL SELECT OrderID, Total FROM dbo.Orders;
`
	lin := extractor.ParseScriptLineage(script)
	if len(lin.Connections) != 1 || lin.Connections[0].Kind != "ODBC" {
		t.Fatalf("unexpected connections: %+v", lin.Connections)
	}
	orders := findTable(lin.Tables, "Orders")
	if orders == nil {
		t.Fatalf("Orders not found: %+v", lin.Tables)
	}
	if orders.SourceType != "sql" || orders.Source != "dbo.Orders" {
		t.Errorf("unexpected sql source: %+v", orders)
	}
	if len(orders.Fields) != 2 || orders.Fields[0] != "OrderID" || orders.Fields[1] != "Total" {
		t.Errorf("unexpected fields: %v", orders.Fields)
	}
}

func TestParseScriptLineage_CommentsAndLibPathPreserved(t *testing.T) {
	script := `
// this is a comment with FROM [notreal.qvd]
/* block
   LOAD ignored FROM [alsofake.qvd]; */
T:
LOAD x FROM [lib://folder/sub/data.qvd] (qvd);
`
	lin := extractor.ParseScriptLineage(script)
	// The commented-out FROMs must not create tables.
	if len(lin.Tables) != 1 {
		t.Fatalf("expected 1 table (comments ignored), got %d: %+v", len(lin.Tables), lin.Tables)
	}
	tt := lin.Tables[0]
	if tt.Name != "T" || tt.SourceType != "qvd" {
		t.Errorf("unexpected table: %+v", tt)
	}
}

func TestParseScriptLineage_Empty(t *testing.T) {
	lin := extractor.ParseScriptLineage("")
	if lin.Connections == nil || lin.Tables == nil {
		t.Error("expected non-nil empty slices")
	}
}
