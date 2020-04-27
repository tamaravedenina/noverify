package expr_test

import (
	"testing"

	"github.com/VKCOM/noverify/src/linttest/assert"

	"github.com/VKCOM/noverify/src/php/parser/node"
	"github.com/VKCOM/noverify/src/php/parser/node/expr"
	"github.com/VKCOM/noverify/src/php/parser/node/stmt"
	"github.com/VKCOM/noverify/src/php/parser/php7"
	"github.com/VKCOM/noverify/src/php/parser/position"
)

func TestTernary(t *testing.T) {
	src := `<? $a ? $b : $c;`

	expected := &node.Root{
		Position: &position.Position{
			StartLine: 1,
			EndLine:   1,
			StartPos:  3,
			EndPos:    16,
		},
		Stmts: []node.Node{
			&stmt.Expression{
				Position: &position.Position{
					StartLine: 1,
					EndLine:   1,
					StartPos:  3,
					EndPos:    16,
				},
				Expr: &expr.Ternary{
					Position: &position.Position{
						StartLine: 1,
						EndLine:   1,
						StartPos:  3,
						EndPos:    15,
					},
					Condition: &node.SimpleVar{
						Position: &position.Position{
							StartLine: 1,
							EndLine:   1,
							StartPos:  3,
							EndPos:    5,
						},
						Name: "a",
					},
					IfTrue: &node.SimpleVar{
						Position: &position.Position{
							StartLine: 1,
							EndLine:   1,
							StartPos:  8,
							EndPos:    10,
						},
						Name: "b",
					},
					IfFalse: &node.SimpleVar{
						Position: &position.Position{
							StartLine: 1,
							EndLine:   1,
							StartPos:  13,
							EndPos:    15,
						},
						Name: "c",
					},
				},
			},
		},
	}

	php7parser := php7.NewParser([]byte(src))
	php7parser.Parse()
	actual := php7parser.GetRootNode()
	assert.DeepEqual(t, expected, actual)
}

func TestTernarySimple(t *testing.T) {
	src := `<? $a ? : $c;`

	expected := &node.Root{
		Position: &position.Position{
			StartLine: 1,
			EndLine:   1,
			StartPos:  3,
			EndPos:    13,
		},
		Stmts: []node.Node{
			&stmt.Expression{
				Position: &position.Position{
					StartLine: 1,
					EndLine:   1,
					StartPos:  3,
					EndPos:    13,
				},
				Expr: &expr.Ternary{
					Position: &position.Position{
						StartLine: 1,
						EndLine:   1,
						StartPos:  3,
						EndPos:    12,
					},
					Condition: &node.SimpleVar{
						Position: &position.Position{
							StartLine: 1,
							EndLine:   1,
							StartPos:  3,
							EndPos:    5,
						},
						Name: "a",
					},
					IfFalse: &node.SimpleVar{
						Position: &position.Position{
							StartLine: 1,
							EndLine:   1,
							StartPos:  10,
							EndPos:    12,
						},
						Name: "c",
					},
				},
			},
		},
	}

	php7parser := php7.NewParser([]byte(src))
	php7parser.Parse()
	actual := php7parser.GetRootNode()
	assert.DeepEqual(t, expected, actual)
}

func TestTernaryNestedTrue(t *testing.T) {
	src := `<? $a ? $b ? $c : $d : $e;`

	expected := &node.Root{
		Position: &position.Position{
			StartLine: 1,
			EndLine:   1,
			StartPos:  3,
			EndPos:    26,
		},
		Stmts: []node.Node{
			&stmt.Expression{
				Position: &position.Position{
					StartLine: 1,
					EndLine:   1,
					StartPos:  3,
					EndPos:    26,
				},
				Expr: &expr.Ternary{
					Position: &position.Position{
						StartLine: 1,
						EndLine:   1,
						StartPos:  3,
						EndPos:    25,
					},
					Condition: &node.SimpleVar{
						Position: &position.Position{
							StartLine: 1,
							EndLine:   1,
							StartPos:  3,
							EndPos:    5,
						},
						Name: "a",
					},
					IfTrue: &expr.Ternary{
						Position: &position.Position{
							StartLine: 1,
							EndLine:   1,
							StartPos:  8,
							EndPos:    20,
						},
						Condition: &node.SimpleVar{
							Position: &position.Position{
								StartLine: 1,
								EndLine:   1,
								StartPos:  8,
								EndPos:    10,
							},
							Name: "b",
						},
						IfTrue: &node.SimpleVar{
							Position: &position.Position{
								StartLine: 1,
								EndLine:   1,
								StartPos:  13,
								EndPos:    15,
							},
							Name: "c",
						},
						IfFalse: &node.SimpleVar{
							Position: &position.Position{
								StartLine: 1,
								EndLine:   1,
								StartPos:  18,
								EndPos:    20,
							},
							Name: "d",
						},
					},
					IfFalse: &node.SimpleVar{
						Position: &position.Position{
							StartLine: 1,
							EndLine:   1,
							StartPos:  23,
							EndPos:    25,
						},
						Name: "e",
					},
				},
			},
		},
	}

	php7parser := php7.NewParser([]byte(src))
	php7parser.Parse()
	actual := php7parser.GetRootNode()
	assert.DeepEqual(t, expected, actual)
}

func TestTernaryNestedCond(t *testing.T) {
	src := `<? $a ? $b : $c ? $d : $e;`

	expected := &node.Root{
		Position: &position.Position{
			StartLine: 1,
			EndLine:   1,
			StartPos:  3,
			EndPos:    26,
		},
		Stmts: []node.Node{
			&stmt.Expression{
				Position: &position.Position{
					StartLine: 1,
					EndLine:   1,
					StartPos:  3,
					EndPos:    26,
				},
				Expr: &expr.Ternary{
					Position: &position.Position{
						StartLine: 1,
						EndLine:   1,
						StartPos:  3,
						EndPos:    25,
					},
					Condition: &expr.Ternary{
						Position: &position.Position{
							StartLine: 1,
							EndLine:   1,
							StartPos:  3,
							EndPos:    15,
						},
						Condition: &node.SimpleVar{
							Position: &position.Position{
								StartLine: 1,
								EndLine:   1,
								StartPos:  3,
								EndPos:    5,
							},
							Name: "a",
						},
						IfTrue: &node.SimpleVar{
							Position: &position.Position{
								StartLine: 1,
								EndLine:   1,
								StartPos:  8,
								EndPos:    10,
							},
							Name: "b",
						},
						IfFalse: &node.SimpleVar{
							Position: &position.Position{
								StartLine: 1,
								EndLine:   1,
								StartPos:  13,
								EndPos:    15,
							},
							Name: "c",
						},
					},
					IfTrue: &node.SimpleVar{
						Position: &position.Position{
							StartLine: 1,
							EndLine:   1,
							StartPos:  18,
							EndPos:    20,
						},
						Name: "d",
					},
					IfFalse: &node.SimpleVar{
						Position: &position.Position{
							StartLine: 1,
							EndLine:   1,
							StartPos:  23,
							EndPos:    25,
						},
						Name: "e",
					},
				},
			},
		},
	}

	php7parser := php7.NewParser([]byte(src))
	php7parser.Parse()
	actual := php7parser.GetRootNode()
	assert.DeepEqual(t, expected, actual)
}
