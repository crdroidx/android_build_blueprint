// Copyright 2016 Google Inc. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package parser

import (
	"fmt"
	"strings"
	"text/scanner"
)

type Node interface {
	// Pos returns the position of the first token in the Node
	Pos() scanner.Position
	// End returns the position of the character after the last token in the Node
	End() scanner.Position
}

// Definition is an Assignment or a Module at the top level of a Blueprints file
type Definition interface {
	Node
	String() string
	definitionTag()
}

// An Assignment is a variable assignment at the top level of a Blueprints file, scoped to the
// file and subdirs.
type Assignment struct {
	Name       string
	NamePos    scanner.Position
	Value      Expression
	OrigValue  Expression
	EqualsPos  scanner.Position
	Assigner   string
	Referenced bool
}

func (a *Assignment) String() string {
	return fmt.Sprintf("%s@%s %s %s (%s) %t", a.Name, a.EqualsPos, a.Assigner, a.Value, a.OrigValue, a.Referenced)
}

func (a *Assignment) Pos() scanner.Position { return a.NamePos }
func (a *Assignment) End() scanner.Position { return a.Value.End() }

func (a *Assignment) definitionTag() {}

// A Module is a module definition at the top level of a Blueprints file
type Module struct {
	Type    string
	TypePos scanner.Position
	Map
	//TODO(delmerico) make this a private field once ag/21588220 lands
	Name__internal_only *string
}

func (m *Module) Copy() *Module {
	ret := *m
	ret.Properties = make([]*Property, len(m.Properties))
	for i := range m.Properties {
		ret.Properties[i] = m.Properties[i].Copy()
	}
	return &ret
}

func (m *Module) String() string {
	propertyStrings := make([]string, len(m.Properties))
	for i, property := range m.Properties {
		propertyStrings[i] = property.String()
	}
	return fmt.Sprintf("%s@%s-%s{%s}", m.Type,
		m.LBracePos, m.RBracePos,
		strings.Join(propertyStrings, ", "))
}

func (m *Module) definitionTag() {}

func (m *Module) Pos() scanner.Position { return m.TypePos }
func (m *Module) End() scanner.Position { return m.Map.End() }

func (m *Module) Name() string {
	if m.Name__internal_only != nil {
		return *m.Name__internal_only
	}
	for _, prop := range m.Properties {
		if prop.Name == "name" {
			if stringProp, ok := prop.Value.(*String); ok {
				name := stringProp.Value
				m.Name__internal_only = &name
			} else {
				name := prop.Value.String()
				m.Name__internal_only = &name
			}
		}
	}
	if m.Name__internal_only == nil {
		name := ""
		m.Name__internal_only = &name
	}
	return *m.Name__internal_only
}

// A Property is a name: value pair within a Map, which may be a top level Module.
type Property struct {
	Name     string
	NamePos  scanner.Position
	ColonPos scanner.Position
	Value    Expression
}

func (p *Property) Copy() *Property {
	ret := *p
	ret.Value = p.Value.Copy()
	return &ret
}

func (p *Property) String() string {
	return fmt.Sprintf("%s@%s: %s", p.Name, p.ColonPos, p.Value)
}

func (p *Property) Pos() scanner.Position { return p.NamePos }
func (p *Property) End() scanner.Position { return p.Value.End() }

// An Expression is a Value in a Property or Assignment.  It can be a literal (String or Bool), a
// Map, a List, an Operator that combines two expressions of the same type, or a Variable that
// references and Assignment.
type Expression interface {
	Node
	// Copy returns a copy of the Expression that will not affect the original if mutated
	Copy() Expression
	String() string
	// Type returns the underlying Type enum of the Expression if it were to be evaluated
	Type() Type
	// Eval returns an expression that is fully evaluated to a simple type (List, Map, String, or
	// Bool).  It will return the same object for every call to Eval().
	Eval() Expression
}

// ExpressionsAreSame tells whether the two values are the same Expression.
// This includes the symbolic representation of each Expression but not their positions in the original source tree.
// This does not apply any simplification to the expressions before comparing them
// (for example, "!!a" wouldn't be deemed equal to "a")
func ExpressionsAreSame(a Expression, b Expression) (equal bool, err error) {
	return hackyExpressionsAreSame(a, b)
}

// TODO(jeffrygaston) once positions are removed from Expression structs,
// remove this function and have callers use reflect.DeepEqual(a, b)
func hackyExpressionsAreSame(a Expression, b Expression) (equal bool, err error) {
	if a.Type() != b.Type() {
		return false, nil
	}
	left, err := hackyFingerprint(a)
	if err != nil {
		return false, nil
	}
	right, err := hackyFingerprint(b)
	if err != nil {
		return false, nil
	}
	areEqual := string(left) == string(right)
	return areEqual, nil
}

func hackyFingerprint(expression Expression) (fingerprint []byte, err error) {
	assignment := &Assignment{"a", noPos, expression, expression, noPos, "=", false}
	module := &File{}
	module.Defs = append(module.Defs, assignment)
	p := newPrinter(module)
	return p.Print()
}

type Type int

const (
	BoolType Type = iota + 1
	StringType
	Int64Type
	ListType
	MapType
	NotEvaluatedType
	UnsetType
)

func (t Type) String() string {
	switch t {
	case BoolType:
		return "bool"
	case StringType:
		return "string"
	case Int64Type:
		return "int64"
	case ListType:
		return "list"
	case MapType:
		return "map"
	case NotEvaluatedType:
		return "notevaluated"
	case UnsetType:
		return "unset"
	default:
		panic(fmt.Errorf("Unknown type %d", t))
	}
}

type Operator struct {
	Args        [2]Expression
	Operator    rune
	OperatorPos scanner.Position
	Value       Expression
}

func (x *Operator) Copy() Expression {
	ret := *x
	ret.Args[0] = x.Args[0].Copy()
	ret.Args[1] = x.Args[1].Copy()
	return &ret
}

func (x *Operator) Eval() Expression {
	return x.Value.Eval()
}

func (x *Operator) Type() Type {
	return x.Args[0].Type()
}

func (x *Operator) Pos() scanner.Position { return x.Args[0].Pos() }
func (x *Operator) End() scanner.Position { return x.Args[1].End() }

func (x *Operator) String() string {
	return fmt.Sprintf("(%s %c %s = %s)@%s", x.Args[0].String(), x.Operator, x.Args[1].String(),
		x.Value, x.OperatorPos)
}

type Variable struct {
	Name    string
	NamePos scanner.Position
	Value   Expression
}

func (x *Variable) Pos() scanner.Position { return x.NamePos }
func (x *Variable) End() scanner.Position { return endPos(x.NamePos, len(x.Name)) }

func (x *Variable) Copy() Expression {
	ret := *x
	return &ret
}

func (x *Variable) Eval() Expression {
	return x.Value.Eval()
}

func (x *Variable) String() string {
	return x.Name + " = " + x.Value.String()
}

func (x *Variable) Type() Type { return x.Value.Type() }

type Map struct {
	LBracePos  scanner.Position
	RBracePos  scanner.Position
	Properties []*Property
}

func (x *Map) Pos() scanner.Position { return x.LBracePos }
func (x *Map) End() scanner.Position { return endPos(x.RBracePos, 1) }

func (x *Map) Copy() Expression {
	ret := *x
	ret.Properties = make([]*Property, len(x.Properties))
	for i := range x.Properties {
		ret.Properties[i] = x.Properties[i].Copy()
	}
	return &ret
}

func (x *Map) Eval() Expression {
	return x
}

func (x *Map) String() string {
	propertyStrings := make([]string, len(x.Properties))
	for i, property := range x.Properties {
		propertyStrings[i] = property.String()
	}
	return fmt.Sprintf("@%s-%s{%s}", x.LBracePos, x.RBracePos,
		strings.Join(propertyStrings, ", "))
}

func (x *Map) Type() Type { return MapType }

// GetProperty looks for a property with the given name.
// It resembles the bracket operator of a built-in Golang map.
func (x *Map) GetProperty(name string) (Property *Property, found bool) {
	prop, found, _ := x.getPropertyImpl(name)
	return prop, found // we don't currently expose the index to callers
}

func (x *Map) getPropertyImpl(name string) (Property *Property, found bool, index int) {
	for i, prop := range x.Properties {
		if prop.Name == name {
			return prop, true, i
		}
	}
	return nil, false, -1
}

// RemoveProperty removes the property with the given name, if it exists.
func (x *Map) RemoveProperty(propertyName string) (removed bool) {
	_, found, index := x.getPropertyImpl(propertyName)
	if found {
		x.Properties = append(x.Properties[:index], x.Properties[index+1:]...)
	}
	return found
}

// MovePropertyContents moves the contents of propertyName into property newLocation
// If property newLocation doesn't exist, MovePropertyContents renames propertyName as newLocation.
// Otherwise, MovePropertyContents only supports moving contents that are a List of String.
func (x *Map) MovePropertyContents(propertyName string, newLocation string) (removed bool) {
	oldProp, oldFound, _ := x.getPropertyImpl(propertyName)
	newProp, newFound, _ := x.getPropertyImpl(newLocation)

	// newLoc doesn't exist, simply renaming property
	if oldFound && !newFound {
		oldProp.Name = newLocation
		return oldFound
	}

	if oldFound {
		old, oldOk := oldProp.Value.(*List)
		new, newOk := newProp.Value.(*List)
		if oldOk && newOk {
			toBeMoved := make([]string, len(old.Values)) //
			for i, p := range old.Values {
				toBeMoved[i] = p.(*String).Value
			}

			for _, moved := range toBeMoved {
				RemoveStringFromList(old, moved)
				AddStringToList(new, moved)
			}
			// oldProp should now be empty and needs to be deleted
			x.RemoveProperty(oldProp.Name)
		} else {
			print(`MovePropertyContents currently only supports moving PropertyName
					with List of Strings into an existing newLocation with List of Strings\n`)
		}
	}
	return oldFound
}

type List struct {
	LBracePos scanner.Position
	RBracePos scanner.Position
	Values    []Expression
}

func (x *List) Pos() scanner.Position { return x.LBracePos }
func (x *List) End() scanner.Position { return endPos(x.RBracePos, 1) }

func (x *List) Copy() Expression {
	ret := *x
	ret.Values = make([]Expression, len(x.Values))
	for i := range ret.Values {
		ret.Values[i] = x.Values[i].Copy()
	}
	return &ret
}

func (x *List) Eval() Expression {
	return x
}

func (x *List) String() string {
	valueStrings := make([]string, len(x.Values))
	for i, value := range x.Values {
		valueStrings[i] = value.String()
	}
	return fmt.Sprintf("@%s-%s[%s]", x.LBracePos, x.RBracePos,
		strings.Join(valueStrings, ", "))
}

func (x *List) Type() Type { return ListType }

type String struct {
	LiteralPos scanner.Position
	Value      string
}

func (x *String) Pos() scanner.Position { return x.LiteralPos }
func (x *String) End() scanner.Position { return endPos(x.LiteralPos, len(x.Value)+2) }

func (x *String) Copy() Expression {
	ret := *x
	return &ret
}

func (x *String) Eval() Expression {
	return x
}

func (x *String) String() string {
	return fmt.Sprintf("%q@%s", x.Value, x.LiteralPos)
}

func (x *String) Type() Type {
	return StringType
}

type Int64 struct {
	LiteralPos scanner.Position
	Value      int64
	Token      string
}

func (x *Int64) Pos() scanner.Position { return x.LiteralPos }
func (x *Int64) End() scanner.Position { return endPos(x.LiteralPos, len(x.Token)) }

func (x *Int64) Copy() Expression {
	ret := *x
	return &ret
}

func (x *Int64) Eval() Expression {
	return x
}

func (x *Int64) String() string {
	return fmt.Sprintf("%q@%s", x.Value, x.LiteralPos)
}

func (x *Int64) Type() Type {
	return Int64Type
}

type Bool struct {
	LiteralPos scanner.Position
	Value      bool
	Token      string
}

func (x *Bool) Pos() scanner.Position { return x.LiteralPos }
func (x *Bool) End() scanner.Position { return endPos(x.LiteralPos, len(x.Token)) }

func (x *Bool) Copy() Expression {
	ret := *x
	return &ret
}

func (x *Bool) Eval() Expression {
	return x
}

func (x *Bool) String() string {
	return fmt.Sprintf("%t@%s", x.Value, x.LiteralPos)
}

func (x *Bool) Type() Type {
	return BoolType
}

type CommentGroup struct {
	Comments []*Comment
}

func (x *CommentGroup) Pos() scanner.Position { return x.Comments[0].Pos() }
func (x *CommentGroup) End() scanner.Position { return x.Comments[len(x.Comments)-1].End() }

type Comment struct {
	Comment []string
	Slash   scanner.Position
}

func (c Comment) Pos() scanner.Position {
	return c.Slash
}

func (c Comment) End() scanner.Position {
	pos := c.Slash
	for _, comment := range c.Comment {
		pos.Offset += len(comment) + 1
		pos.Column = len(comment) + 1
	}
	pos.Line += len(c.Comment) - 1
	return pos
}

func (c Comment) String() string {
	l := 0
	for _, comment := range c.Comment {
		l += len(comment) + 1
	}
	buf := make([]byte, 0, l)
	for _, comment := range c.Comment {
		buf = append(buf, comment...)
		buf = append(buf, '\n')
	}

	return string(buf) + "@" + c.Slash.String()
}

// Return the text of the comment with // or /* and */ stripped
func (c Comment) Text() string {
	l := 0
	for _, comment := range c.Comment {
		l += len(comment) + 1
	}
	buf := make([]byte, 0, l)

	blockComment := false
	if strings.HasPrefix(c.Comment[0], "/*") {
		blockComment = true
	}

	for i, comment := range c.Comment {
		if blockComment {
			if i == 0 {
				comment = strings.TrimPrefix(comment, "/*")
			}
			if i == len(c.Comment)-1 {
				comment = strings.TrimSuffix(comment, "*/")
			}
		} else {
			comment = strings.TrimPrefix(comment, "//")
		}
		buf = append(buf, comment...)
		buf = append(buf, '\n')
	}

	return string(buf)
}

type NotEvaluated struct {
	Position scanner.Position
}

func (n NotEvaluated) Copy() Expression {
	return NotEvaluated{Position: n.Position}
}

func (n NotEvaluated) String() string {
	return "Not Evaluated"
}

func (n NotEvaluated) Type() Type {
	return NotEvaluatedType
}

func (n NotEvaluated) Eval() Expression {
	return NotEvaluated{Position: n.Position}
}

func (n NotEvaluated) Pos() scanner.Position { return n.Position }
func (n NotEvaluated) End() scanner.Position { return n.Position }

func endPos(pos scanner.Position, n int) scanner.Position {
	pos.Offset += n
	pos.Column += n
	return pos
}

type ConfigurableCondition struct {
	position     scanner.Position
	FunctionName string
	Args         []String
}

func (c *ConfigurableCondition) Equals(other ConfigurableCondition) bool {
	if c.FunctionName != other.FunctionName {
		return false
	}
	if len(c.Args) != len(other.Args) {
		return false
	}
	for i := range c.Args {
		if c.Args[i] != other.Args[i] {
			return false
		}
	}
	return true
}

func (c *ConfigurableCondition) String() string {
	var sb strings.Builder
	sb.WriteString(c.FunctionName)
	sb.WriteRune('(')
	for i, arg := range c.Args {
		sb.WriteRune('"')
		sb.WriteString(arg.Value)
		sb.WriteRune('"')
		if i < len(c.Args)-1 {
			sb.WriteString(", ")
		}
	}
	sb.WriteRune(')')
	return sb.String()
}

type Select struct {
	KeywordPos     scanner.Position // the keyword "select"
	Conditions     []ConfigurableCondition
	LBracePos      scanner.Position
	RBracePos      scanner.Position
	Cases          []*SelectCase // the case statements
	Append         Expression
	ExpressionType Type
}

func (s *Select) Pos() scanner.Position { return s.KeywordPos }
func (s *Select) End() scanner.Position { return endPos(s.RBracePos, 1) }

func (s *Select) Copy() Expression {
	ret := *s
	ret.Cases = make([]*SelectCase, len(ret.Cases))
	for i, selectCase := range s.Cases {
		ret.Cases[i] = selectCase.Copy()
	}
	if s.Append != nil {
		ret.Append = s.Append.Copy()
	}
	return &ret
}

func (s *Select) Eval() Expression {
	return s
}

func (s *Select) String() string {
	return "<select>"
}

func (s *Select) Type() Type {
	if s.ExpressionType == UnsetType && s.Append != nil {
		return s.Append.Type()
	}
	return s.ExpressionType
}

type SelectCase struct {
	Patterns []Expression
	ColonPos scanner.Position
	Value    Expression
}

func (c *SelectCase) Copy() *SelectCase {
	ret := *c
	ret.Value = c.Value.Copy()
	return &ret
}

func (c *SelectCase) String() string {
	return "<select case>"
}

func (c *SelectCase) Pos() scanner.Position { return c.Patterns[0].Pos() }
func (c *SelectCase) End() scanner.Position { return c.Value.End() }

// UnsetProperty is the expression type of the "unset" keyword that can be
// used in select statements to make the property unset. For example:
//
//	my_module_type {
//	  name: "foo",
//	  some_prop: select(soong_config_variable("my_namespace", "my_var"), {
//	    "foo": unset,
//	    "default": "bar",
//	  })
//	}
type UnsetProperty struct {
	Position scanner.Position
}

func (n UnsetProperty) Copy() Expression {
	return UnsetProperty{Position: n.Position}
}

func (n UnsetProperty) String() string {
	return "unset"
}

func (n UnsetProperty) Type() Type {
	return UnsetType
}

func (n UnsetProperty) Eval() Expression {
	return UnsetProperty{Position: n.Position}
}

func (n UnsetProperty) Pos() scanner.Position { return n.Position }
func (n UnsetProperty) End() scanner.Position { return n.Position }
