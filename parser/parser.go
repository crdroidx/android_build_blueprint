// Copyright 2014 Google Inc. All rights reserved.
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
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"text/scanner"
)

var errTooManyErrors = errors.New("too many errors")

const maxErrors = 1

const default_select_branch_name = "__soong_conditions_default__"

type ParseError struct {
	Err error
	Pos scanner.Position
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("%s: %s", e.Pos, e.Err)
}

type File struct {
	Name     string
	Defs     []Definition
	Comments []*CommentGroup
}

func (f *File) Pos() scanner.Position {
	return scanner.Position{
		Filename: f.Name,
		Line:     1,
		Column:   1,
		Offset:   0,
	}
}

func (f *File) End() scanner.Position {
	if len(f.Defs) > 0 {
		return f.Defs[len(f.Defs)-1].End()
	}
	return noPos
}

func parse(p *parser) (file *File, errs []error) {
	defer func() {
		if r := recover(); r != nil {
			if r == errTooManyErrors {
				errs = p.errors
				return
			}
			panic(r)
		}
	}()

	p.next()
	defs := p.parseDefinitions()
	p.accept(scanner.EOF)
	errs = p.errors
	comments := p.comments

	return &File{
		Name:     p.scanner.Filename,
		Defs:     defs,
		Comments: comments,
	}, errs

}

func ParseAndEval(filename string, r io.Reader, scope *Scope) (file *File, errs []error) {
	p := newParser(r, scope)
	p.eval = true
	p.scanner.Filename = filename

	return parse(p)
}

func Parse(filename string, r io.Reader, scope *Scope) (file *File, errs []error) {
	p := newParser(r, scope)
	p.scanner.Filename = filename

	return parse(p)
}

func ParseExpression(r io.Reader) (value Expression, errs []error) {
	p := newParser(r, NewScope(nil))
	p.next()
	value = p.parseExpression()
	p.accept(scanner.EOF)
	errs = p.errors
	return
}

type parser struct {
	scanner  scanner.Scanner
	tok      rune
	errors   []error
	scope    *Scope
	comments []*CommentGroup
	eval     bool
}

func newParser(r io.Reader, scope *Scope) *parser {
	p := &parser{}
	p.scope = scope
	p.scanner.Init(r)
	p.scanner.Error = func(sc *scanner.Scanner, msg string) {
		p.errorf(msg)
	}
	p.scanner.Mode = scanner.ScanIdents | scanner.ScanInts | scanner.ScanStrings |
		scanner.ScanRawStrings | scanner.ScanComments
	return p
}

func (p *parser) error(err error) {
	pos := p.scanner.Position
	if !pos.IsValid() {
		pos = p.scanner.Pos()
	}
	err = &ParseError{
		Err: err,
		Pos: pos,
	}
	p.errors = append(p.errors, err)
	if len(p.errors) >= maxErrors {
		panic(errTooManyErrors)
	}
}

func (p *parser) errorf(format string, args ...interface{}) {
	p.error(fmt.Errorf(format, args...))
}

func (p *parser) accept(toks ...rune) bool {
	for _, tok := range toks {
		if p.tok != tok {
			p.errorf("expected %s, found %s", scanner.TokenString(tok),
				scanner.TokenString(p.tok))
			return false
		}
		p.next()
	}
	return true
}

func (p *parser) next() {
	if p.tok != scanner.EOF {
		p.tok = p.scanner.Scan()
		if p.tok == scanner.Comment {
			var comments []*Comment
			for p.tok == scanner.Comment {
				lines := strings.Split(p.scanner.TokenText(), "\n")
				if len(comments) > 0 && p.scanner.Position.Line > comments[len(comments)-1].End().Line+1 {
					p.comments = append(p.comments, &CommentGroup{Comments: comments})
					comments = nil
				}
				comments = append(comments, &Comment{lines, p.scanner.Position})
				p.tok = p.scanner.Scan()
			}
			p.comments = append(p.comments, &CommentGroup{Comments: comments})
		}
	}
}

func (p *parser) parseDefinitions() (defs []Definition) {
	for {
		switch p.tok {
		case scanner.Ident:
			ident := p.scanner.TokenText()
			pos := p.scanner.Position

			p.accept(scanner.Ident)

			switch p.tok {
			case '+':
				p.accept('+')
				defs = append(defs, p.parseAssignment(ident, pos, "+="))
			case '=':
				defs = append(defs, p.parseAssignment(ident, pos, "="))
			case '{', '(':
				defs = append(defs, p.parseModule(ident, pos))
			default:
				p.errorf("expected \"=\" or \"+=\" or \"{\" or \"(\", found %s",
					scanner.TokenString(p.tok))
			}
		case scanner.EOF:
			return
		default:
			p.errorf("expected assignment or module definition, found %s",
				scanner.TokenString(p.tok))
			return
		}
	}
}

func (p *parser) parseAssignment(name string, namePos scanner.Position,
	assigner string) (assignment *Assignment) {

	// These are used as keywords in select statements, prevent making variables
	// with the same name to avoid any confusion.
	switch name {
	case "default", "unset":
		p.errorf("'default' and 'unset' are reserved keywords, and cannot be used as variable names")
		return nil
	}

	assignment = new(Assignment)

	pos := p.scanner.Position
	if !p.accept('=') {
		return
	}
	value := p.parseExpression()

	assignment.Name = name
	assignment.NamePos = namePos
	assignment.Value = value
	assignment.OrigValue = value
	assignment.EqualsPos = pos
	assignment.Assigner = assigner

	if p.scope != nil {
		if assigner == "+=" {
			if old, local := p.scope.Get(assignment.Name); old == nil {
				p.errorf("modified non-existent variable %q with +=", assignment.Name)
			} else if !local {
				p.errorf("modified non-local variable %q with +=", assignment.Name)
			} else if old.Referenced {
				p.errorf("modified variable %q with += after referencing", assignment.Name)
			} else {
				val, err := p.evaluateOperator(old.Value, assignment.Value, '+', assignment.EqualsPos)
				if err != nil {
					p.error(err)
				} else {
					old.Value = val
				}
			}
		} else {
			err := p.scope.Add(assignment)
			if err != nil {
				p.error(err)
			}
		}
	}

	return
}

func (p *parser) parseModule(typ string, typPos scanner.Position) *Module {

	compat := false
	lbracePos := p.scanner.Position
	if p.tok == '{' {
		compat = true
	}

	if !p.accept(p.tok) {
		return nil
	}
	properties := p.parsePropertyList(true, compat)
	rbracePos := p.scanner.Position
	if !compat {
		p.accept(')')
	} else {
		p.accept('}')
	}

	return &Module{
		Type:    typ,
		TypePos: typPos,
		Map: Map{
			Properties: properties,
			LBracePos:  lbracePos,
			RBracePos:  rbracePos,
		},
	}
}

func (p *parser) parsePropertyList(isModule, compat bool) (properties []*Property) {
	for p.tok == scanner.Ident {
		property := p.parseProperty(isModule, compat)

		// If a property is set to an empty select or a select where all branches are "unset",
		// skip emitting the property entirely.
		if property.Value.Type() != UnsetType {
			properties = append(properties, property)
		}

		if p.tok != ',' {
			// There was no comma, so the list is done.
			break
		}

		p.accept(',')
	}

	return
}

func (p *parser) parseProperty(isModule, compat bool) (property *Property) {
	property = new(Property)

	name := p.scanner.TokenText()
	namePos := p.scanner.Position
	p.accept(scanner.Ident)
	pos := p.scanner.Position

	if isModule {
		if compat {
			if !p.accept(':') {
				return
			}
		} else {
			if !p.accept('=') {
				return
			}
		}
	} else {
		if !p.accept(':') {
			return
		}
	}

	value := p.parseExpression()

	property.Name = name
	property.NamePos = namePos
	property.Value = value
	property.ColonPos = pos

	return
}

func (p *parser) parseExpression() (value Expression) {
	value = p.parseValue()
	switch p.tok {
	case '+':
		return p.parseOperator(value)
	case '-':
		p.errorf("subtraction not supported: %s", p.scanner.String())
		return value
	default:
		return value
	}
}

func (p *parser) evaluateOperator(value1, value2 Expression, operator rune,
	pos scanner.Position) (Expression, error) {

	if value1.Type() == UnsetType {
		return value2, nil
	}
	if value2.Type() == UnsetType {
		return value1, nil
	}

	value := value1

	if p.eval {
		e1 := value1.Eval()
		e2 := value2.Eval()
		if e1.Type() != e2.Type() {
			return nil, fmt.Errorf("mismatched type in operator %c: %s != %s", operator,
				e1.Type(), e2.Type())
		}

		if _, ok := e1.(*Select); !ok {
			if _, ok := e2.(*Select); ok {
				// Promote e1 to a select so we can add e2 to it
				e1 = &Select{
					Cases: []*SelectCase{{
						Value: e1,
					}},
					ExpressionType: e1.Type(),
				}
			}
		}

		value = e1.Copy()

		switch operator {
		case '+':
			switch v := value.(type) {
			case *String:
				v.Value += e2.(*String).Value
			case *Int64:
				v.Value += e2.(*Int64).Value
				v.Token = ""
			case *List:
				v.Values = append(v.Values, e2.(*List).Values...)
			case *Map:
				var err error
				v.Properties, err = p.addMaps(v.Properties, e2.(*Map).Properties, pos)
				if err != nil {
					return nil, err
				}
			case *Select:
				v.Append = e2
			default:
				return nil, fmt.Errorf("operator %c not supported on type %s", operator, v.Type())
			}
		default:
			panic("unknown operator " + string(operator))
		}
	}

	return &Operator{
		Args:        [2]Expression{value1, value2},
		Operator:    operator,
		OperatorPos: pos,
		Value:       value,
	}, nil
}

func (p *parser) addMaps(map1, map2 []*Property, pos scanner.Position) ([]*Property, error) {
	ret := make([]*Property, 0, len(map1))

	inMap1 := make(map[string]*Property)
	inMap2 := make(map[string]*Property)
	inBoth := make(map[string]*Property)

	for _, prop1 := range map1 {
		inMap1[prop1.Name] = prop1
	}

	for _, prop2 := range map2 {
		inMap2[prop2.Name] = prop2
		if _, ok := inMap1[prop2.Name]; ok {
			inBoth[prop2.Name] = prop2
		}
	}

	for _, prop1 := range map1 {
		if prop2, ok := inBoth[prop1.Name]; ok {
			var err error
			newProp := *prop1
			newProp.Value, err = p.evaluateOperator(prop1.Value, prop2.Value, '+', pos)
			if err != nil {
				return nil, err
			}
			ret = append(ret, &newProp)
		} else {
			ret = append(ret, prop1)
		}
	}

	for _, prop2 := range map2 {
		if _, ok := inBoth[prop2.Name]; !ok {
			ret = append(ret, prop2)
		}
	}

	return ret, nil
}

func (p *parser) parseOperator(value1 Expression) Expression {
	operator := p.tok
	pos := p.scanner.Position
	p.accept(operator)

	value2 := p.parseExpression()

	value, err := p.evaluateOperator(value1, value2, operator, pos)
	if err != nil {
		p.error(err)
		return nil
	}

	return value

}

func (p *parser) parseValue() (value Expression) {
	switch p.tok {
	case scanner.Ident:
		switch text := p.scanner.TokenText(); text {
		case "true", "false":
			return p.parseBoolean()
		case "select":
			return p.parseSelect()
		default:
			return p.parseVariable()
		}
	case '-', scanner.Int: // Integer might have '-' sign ahead ('+' is only treated as operator now)
		return p.parseIntValue()
	case scanner.String, scanner.RawString:
		return p.parseStringValue()
	case '[':
		return p.parseListValue()
	case '{':
		return p.parseMapValue()
	default:
		p.errorf("expected bool, list, or string value; found %s",
			scanner.TokenString(p.tok))
		return
	}
}

func (p *parser) parseBoolean() Expression {
	switch text := p.scanner.TokenText(); text {
	case "true", "false":
		result := &Bool{
			LiteralPos: p.scanner.Position,
			Value:      text == "true",
			Token:      text,
		}
		p.accept(scanner.Ident)
		return result
	default:
		p.errorf("Expected true/false, got %q", text)
		return nil
	}
}

func (p *parser) parseVariable() Expression {
	var value Expression

	text := p.scanner.TokenText()
	if p.eval {
		if assignment, local := p.scope.Get(text); assignment == nil {
			p.errorf("variable %q is not set", text)
		} else {
			if local {
				assignment.Referenced = true
			}
			value = assignment.Value
		}
	} else {
		value = &NotEvaluated{}
	}
	value = &Variable{
		Name:    text,
		NamePos: p.scanner.Position,
		Value:   value,
	}

	p.accept(scanner.Ident)
	return value
}

func (p *parser) parseSelect() Expression {
	result := &Select{
		KeywordPos: p.scanner.Position,
	}
	// Read the "select("
	p.accept(scanner.Ident)
	if !p.accept('(') {
		return nil
	}

	// If we see another '(', there's probably multiple conditions and there must
	// be a ')' after. Set the multipleConditions variable to remind us to check for
	// the ')' after.
	multipleConditions := false
	if p.tok == '(' {
		multipleConditions = true
		p.accept('(')
	}

	// Read all individual conditions
	conditions := []ConfigurableCondition{}
	for first := true; first || multipleConditions; first = false {
		condition := ConfigurableCondition{
			position:     p.scanner.Position,
			FunctionName: p.scanner.TokenText(),
		}
		if !p.accept(scanner.Ident) {
			return nil
		}
		if !p.accept('(') {
			return nil
		}

		for p.tok != ')' {
			if s := p.parseStringValue(); s != nil {
				condition.Args = append(condition.Args, *s)
			} else {
				return nil
			}
			if p.tok == ')' {
				break
			}
			if !p.accept(',') {
				return nil
			}
		}
		p.accept(')')

		for _, c := range conditions {
			if c.Equals(condition) {
				p.errorf("Duplicate select condition found: %s", c.String())
			}
		}

		conditions = append(conditions, condition)

		if multipleConditions {
			if p.tok == ')' {
				p.next()
				break
			}
			if !p.accept(',') {
				return nil
			}
			// Retry the closing parent to allow for a trailing comma
			if p.tok == ')' {
				p.next()
				break
			}
		}
	}

	if multipleConditions && len(conditions) < 2 {
		p.errorf("Expected multiple select conditions due to the extra parenthesis, but only found 1. Please remove the extra parenthesis.")
		return nil
	}

	result.Conditions = conditions

	if !p.accept(',') {
		return nil
	}

	result.LBracePos = p.scanner.Position
	if !p.accept('{') {
		return nil
	}

	parseOnePattern := func() Expression {
		switch p.tok {
		case scanner.Ident:
			switch p.scanner.TokenText() {
			case "default":
				p.next()
				return &String{
					LiteralPos: p.scanner.Position,
					Value:      default_select_branch_name,
				}
			case "true":
				p.next()
				return &Bool{
					LiteralPos: p.scanner.Position,
					Value:      true,
				}
			case "false":
				p.next()
				return &Bool{
					LiteralPos: p.scanner.Position,
					Value:      false,
				}
			default:
				p.errorf("Expted a string, true, false, or default, got %s", p.scanner.TokenText())
			}
		case scanner.String:
			if s := p.parseStringValue(); s != nil {
				if strings.HasPrefix(s.Value, "__soong") {
					p.errorf("select branch conditions starting with __soong are reserved for internal use")
					return nil
				}
				return s
			}
			fallthrough
		default:
			p.errorf("Expted a string, true, false, or default, got %s", p.scanner.TokenText())
		}
		return nil
	}

	hasNonUnsetValue := false
	for p.tok != '}' {
		c := &SelectCase{}

		if multipleConditions {
			if !p.accept('(') {
				return nil
			}
			for i := 0; i < len(conditions); i++ {
				if p := parseOnePattern(); p != nil {
					c.Patterns = append(c.Patterns, p)
				} else {
					return nil
				}
				if i < len(conditions)-1 {
					if !p.accept(',') {
						return nil
					}
				} else if p.tok == ',' {
					// allow optional trailing comma
					p.next()
				}
			}
			if !p.accept(')') {
				return nil
			}
		} else {
			if p := parseOnePattern(); p != nil {
				c.Patterns = append(c.Patterns, p)
			} else {
				return nil
			}
		}
		c.ColonPos = p.scanner.Position
		if !p.accept(':') {
			return nil
		}
		if p.tok == scanner.Ident && p.scanner.TokenText() == "unset" {
			c.Value = UnsetProperty{Position: p.scanner.Position}
			p.accept(scanner.Ident)
		} else {
			hasNonUnsetValue = true
			c.Value = p.parseExpression()
		}
		if !p.accept(',') {
			return nil
		}
		result.Cases = append(result.Cases, c)
	}

	// If all branches have the value "unset", then this is equivalent
	// to an empty select.
	if !hasNonUnsetValue {
		p.errorf("This select statement is empty, remove it")
		return nil
	}

	patternsEqual := func(a, b Expression) bool {
		switch a2 := a.(type) {
		case *String:
			if b2, ok := b.(*String); ok {
				return a2.Value == b2.Value
			} else {
				return false
			}
		case *Bool:
			if b2, ok := b.(*Bool); ok {
				return a2.Value == b2.Value
			} else {
				return false
			}
		default:
			// true so that we produce an error in this unexpected scenario
			return true
		}
	}

	patternListsEqual := func(a, b []Expression) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if !patternsEqual(a[i], b[i]) {
				return false
			}
		}
		return true
	}

	for i, c := range result.Cases {
		// Check for duplicates
		for _, d := range result.Cases[i+1:] {
			if patternListsEqual(c.Patterns, d.Patterns) {
				p.errorf("Found duplicate select patterns: %v", c.Patterns)
				return nil
			}
		}
		// Check that the only all-default cases is the last one
		if i < len(result.Cases)-1 {
			isAllDefault := true
			for _, x := range c.Patterns {
				if x2, ok := x.(*String); !ok || x2.Value != default_select_branch_name {
					isAllDefault = false
					break
				}
			}
			if isAllDefault {
				p.errorf("Found a default select branch at index %d, expected it to be last (index %d)", i, len(result.Cases)-1)
				return nil
			}
		}
	}

	ty := UnsetType
	for _, c := range result.Cases {
		otherTy := c.Value.Type()
		// Any other type can override UnsetType
		if ty == UnsetType {
			ty = otherTy
		}
		if otherTy != UnsetType && otherTy != ty {
			p.errorf("Found select statement with differing types %q and %q in its cases", ty.String(), otherTy.String())
			return nil
		}
	}

	result.ExpressionType = ty

	result.RBracePos = p.scanner.Position
	if !p.accept('}') {
		return nil
	}
	if !p.accept(')') {
		return nil
	}
	return result
}

func (p *parser) parseStringValue() *String {
	str, err := strconv.Unquote(p.scanner.TokenText())
	if err != nil {
		p.errorf("couldn't parse string: %s", err)
		return nil
	}

	value := &String{
		LiteralPos: p.scanner.Position,
		Value:      str,
	}
	p.accept(p.tok)
	return value
}

func (p *parser) parseIntValue() *Int64 {
	var str string
	literalPos := p.scanner.Position
	if p.tok == '-' {
		str += string(p.tok)
		p.accept(p.tok)
		if p.tok != scanner.Int {
			p.errorf("expected int; found %s", scanner.TokenString(p.tok))
			return nil
		}
	}
	str += p.scanner.TokenText()
	i, err := strconv.ParseInt(str, 10, 64)
	if err != nil {
		p.errorf("couldn't parse int: %s", err)
		return nil
	}

	value := &Int64{
		LiteralPos: literalPos,
		Value:      i,
		Token:      str,
	}
	p.accept(scanner.Int)
	return value
}

func (p *parser) parseListValue() *List {
	lBracePos := p.scanner.Position
	if !p.accept('[') {
		return nil
	}

	var elements []Expression
	for p.tok != ']' {
		element := p.parseExpression()
		elements = append(elements, element)

		if p.tok != ',' {
			// There was no comma, so the list is done.
			break
		}

		p.accept(',')
	}

	rBracePos := p.scanner.Position
	p.accept(']')

	return &List{
		LBracePos: lBracePos,
		RBracePos: rBracePos,
		Values:    elements,
	}
}

func (p *parser) parseMapValue() *Map {
	lBracePos := p.scanner.Position
	if !p.accept('{') {
		return nil
	}

	properties := p.parsePropertyList(false, false)

	rBracePos := p.scanner.Position
	p.accept('}')

	return &Map{
		LBracePos:  lBracePos,
		RBracePos:  rBracePos,
		Properties: properties,
	}
}

type Scope struct {
	vars          map[string]*Assignment
	inheritedVars map[string]*Assignment
}

func NewScope(s *Scope) *Scope {
	newScope := &Scope{
		vars:          make(map[string]*Assignment),
		inheritedVars: make(map[string]*Assignment),
	}

	if s != nil {
		for k, v := range s.vars {
			newScope.inheritedVars[k] = v
		}
		for k, v := range s.inheritedVars {
			newScope.inheritedVars[k] = v
		}
	}

	return newScope
}

func (s *Scope) Add(assignment *Assignment) error {
	if old, ok := s.vars[assignment.Name]; ok {
		return fmt.Errorf("variable already set, previous assignment: %s", old)
	}

	if old, ok := s.inheritedVars[assignment.Name]; ok {
		return fmt.Errorf("variable already set in inherited scope, previous assignment: %s", old)
	}

	s.vars[assignment.Name] = assignment

	return nil
}

func (s *Scope) Remove(name string) {
	delete(s.vars, name)
	delete(s.inheritedVars, name)
}

func (s *Scope) Get(name string) (*Assignment, bool) {
	if a, ok := s.vars[name]; ok {
		return a, true
	}

	if a, ok := s.inheritedVars[name]; ok {
		return a, false
	}

	return nil, false
}

func (s *Scope) String() string {
	vars := []string{}

	for k := range s.vars {
		vars = append(vars, k)
	}
	for k := range s.inheritedVars {
		vars = append(vars, k)
	}

	sort.Strings(vars)

	ret := []string{}
	for _, v := range vars {
		if assignment, ok := s.vars[v]; ok {
			ret = append(ret, assignment.String())
		} else {
			ret = append(ret, s.inheritedVars[v].String())
		}
	}

	return strings.Join(ret, "\n")
}
