package linter

import (
	"bytes"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/VKCOM/noverify/src/git"
	"github.com/VKCOM/noverify/src/meta"
	"github.com/VKCOM/noverify/src/phpdoc"
	"github.com/VKCOM/noverify/src/solver"
	"github.com/VKCOM/noverify/src/state"
	"github.com/VKCOM/noverify/src/vscode"
	"github.com/z7zmey/php-parser/freefloating"
	"github.com/z7zmey/php-parser/node"
	"github.com/z7zmey/php-parser/node/expr"
	"github.com/z7zmey/php-parser/node/expr/assign"
	"github.com/z7zmey/php-parser/node/name"
	"github.com/z7zmey/php-parser/node/scalar"
	"github.com/z7zmey/php-parser/node/stmt"
	"github.com/z7zmey/php-parser/php7"
	"github.com/z7zmey/php-parser/position"
	"github.com/z7zmey/php-parser/walker"
)

const (
	maxFunctionLines = 150
)

// RootWalker is used to analyze root scope. Mostly defines, function and class definitions are analyzed.
type RootWalker struct {
	// autoGenerated is set to true when visiting auto-generated files.
	autoGenerated bool

	filename string

	lineRanges []git.LineRange

	custom      []RootChecker
	customBlock []BlockCheckerCreateFunc
	customState map[string]interface{}

	// internal state
	meta fileMeta

	st               *meta.ClassParseState
	currentClassNode node.Node

	disabledFlag bool // user-defined flag that file should not be linted

	reports []*Report

	// state required for both language server and reports creation
	Lines          [][]byte
	LinesPositions []int

	// exposed meta-information for language server to use
	Scopes      map[node.Node]*meta.Scope
	Diagnostics []vscode.Diagnostic
}

func (d *RootWalker) EnterChildNode(key string, w walker.Walkable) {}
func (d *RootWalker) LeaveChildNode(key string, w walker.Walkable) {}
func (d *RootWalker) EnterChildList(key string, w walker.Walkable) {}
func (d *RootWalker) LeaveChildList(key string, w walker.Walkable) {}

type phpDocParamEl struct {
	optional bool
	typ      *meta.TypesMap
}

type phpDocParamsMap map[string]phpDocParamEl

// NewWalkerForLangServer creates a copy of RootWalker to make full analysis of a file
func NewWalkerForLangServer(prev *RootWalker) *RootWalker {
	return &RootWalker{
		filename:       prev.filename,
		LinesPositions: prev.LinesPositions,
		Lines:          prev.Lines,
		lineRanges:     prev.lineRanges,
		st:             &meta.ClassParseState{},
		autoGenerated:  prev.autoGenerated,
	}
}

// NewWalkerForReferencesSearcher allows to access full context of a parser so that we can perform complex
// searches if needed.
func NewWalkerForReferencesSearcher(filename string, block BlockCheckerCreateFunc) *RootWalker {
	d := &RootWalker{
		filename:    filename,
		st:          &meta.ClassParseState{},
		customBlock: []BlockCheckerCreateFunc{block},
	}
	return d
}

// InitFromParser initializes common fields that are needed for RootWalker work
func (d *RootWalker) InitFromParser(contents []byte, parser *php7.Parser) {
	lines := bytes.Split(contents, []byte("\n"))
	linesPositions := make([]int, len(lines))
	pos := 0
	for idx, ln := range lines {
		linesPositions[idx] = pos
		pos += len(ln) + 1
	}

	d.LinesPositions = linesPositions
	d.Lines = lines
	d.autoGenerated = d.fileIsAutoGenerated(lines)
}

func (d *RootWalker) fileIsAutoGenerated(lines [][]byte) bool {
	// Since we don't have a separate comments list, it's easier
	// to check few leading lines manually. We might want to have a
	// more reliable way to handle comments in future.
	// See #112.
	maxLinesPeek := 10
	if len(lines) < maxLinesPeek {
		maxLinesPeek = len(lines)
	}
	doNotEdit := false
	autoGenerated := false
	for _, l := range lines[:maxLinesPeek] {
		s := strings.ToLower(string(l))
		looksLikeComment := strings.HasPrefix(s, "//") ||
			strings.HasPrefix(s, "/*") ||
			strings.HasPrefix(s, " *")
		if !looksLikeComment {
			continue
		}

		if strings.Contains(s, "do not edit") {
			doNotEdit = true
		}
		if strings.Contains(s, "auto-generated") ||
			strings.Contains(s, "autogenerated") ||
			strings.Contains(s, "generated by") {
			autoGenerated = true
		}

		if doNotEdit && autoGenerated {
			return true
		}
	}
	return false
}

// InitCustom is needed to initialize walker state
func (d *RootWalker) InitCustom() {
	d.custom = nil
	for _, createFn := range customRootLinters {
		d.custom = append(d.custom, createFn(&RootContext{w: d}))
	}

	d.customBlock = customBlockLinters
}

// UpdateMetaInfo is intended to be used in tests. Do not use it directly!
func (d *RootWalker) UpdateMetaInfo() {
	updateMetaInfo(d.filename, &d.meta)
}

// Scope returns root-level variable scope if applicable.
func (d *RootWalker) Scope() *meta.Scope {
	if d.meta.Scope == nil {
		d.meta.Scope = meta.NewScope()
	}
	return d.meta.Scope
}

// State allows for custom hooks to store state between entering root context and block context.
func (d *RootWalker) State() map[string]interface{} {
	if d.customState == nil {
		d.customState = make(map[string]interface{})
	}
	return d.customState
}

// GetReports returns collected reports for this file.
func (d *RootWalker) GetReports() []*Report {
	return d.reports
}

// ClassParseState returns class parse state (namespace, current class, etc)
func (d *RootWalker) ClassParseState() *meta.ClassParseState {
	return d.st
}

// EnterNode is invoked at every node in hierarchy
func (d *RootWalker) EnterNode(w walker.Walkable) (res bool) {
	res = true

	for _, c := range d.custom {
		c.BeforeEnterNode(w)
	}

	if n, ok := w.(node.Node); ok {
		if ffs := n.GetFreeFloating(); ffs != nil {
			for _, cs := range *ffs {
				for _, c := range cs {
					if c.StringType == freefloating.CommentType {
						d.handleComment(c)
					}
				}
			}
		}
	}

	state.EnterNode(d.st, w)

	switch n := w.(type) {
	case *stmt.Interface:
		d.currentClassNode = n
	case *stmt.Class:
		d.currentClassNode = n
		cl := d.getClass()
		if n.Implements != nil {
			for _, tr := range n.Implements.InterfaceNames {
				interfaceName, ok := solver.GetClassName(d.st, tr)
				if ok {
					cl.Interfaces[interfaceName] = struct{}{}
				}
			}
		}
		doc := d.parsePHPDocClass(n.PhpDocComment)
		d.reportPhpdocErrors(n.ClassName, doc.errs)
		// If we ever need to distinguish @property-annotated and real properties,
		// more work will be required here.
		for name, p := range doc.properties {
			p.Pos = cl.Pos
			cl.Properties[name] = p
		}

	case *stmt.Trait:
		d.currentClassNode = n
	case *stmt.TraitUse:
		cl := d.getClass()
		for _, tr := range n.Traits {
			traitName, ok := solver.GetClassName(d.st, tr)
			if ok {
				cl.Traits[traitName] = struct{}{}
			}
		}
	case *assign.Assign:
		v, ok := n.Variable.(*expr.Variable)
		if !ok {
			break
		}

		d.Scope().AddVar(v, solver.ExprTypeLocal(d.meta.Scope, d.st, n.Expression), "global variable", true)
	case *stmt.Function:
		res = d.enterFunction(n)
	case *stmt.PropertyList:
		res = d.enterPropertyList(n)
	case *stmt.ClassConstList:
		res = d.enterClassConstList(n)
	case *stmt.ClassMethod:
		res = d.enterClassMethod(n)
	case *expr.FunctionCall:
		res = d.enterFunctionCall(n)
	case *stmt.ConstList:
		res = d.enterConstList(n)
	}

	for _, c := range d.custom {
		c.AfterEnterNode(w)
	}

	if !res {
		// If we're not returning true from this method,
		// LeaveNode will not be called for this node.
		// But we still need to "leave" them if they
		// were entered in the ClassParseState.
		state.LeaveNode(d.st, w)
	}
	return res
}

func (d *RootWalker) parseStartPos(pos *position.Position) (startLn []byte, startChar int) {
	if pos.StartLine >= 1 && len(d.Lines) > pos.StartLine {
		startLn = d.Lines[pos.StartLine-1]
		p := d.LinesPositions[pos.StartLine-1]
		if pos.StartPos > p {
			startChar = pos.StartPos - p - 1
		}
	}

	return startLn, startChar
}

// Report registers a single report message about some found problem.
func (d *RootWalker) Report(n node.Node, level int, checkName, msg string, args ...interface{}) {
	if !meta.IsIndexingComplete() {
		return
	}
	if d.autoGenerated && !CheckAutoGenerated {
		return
	}

	var pos position.Position

	if n == nil {
		// Hack to parse syntax error message from php-parser.
		// When in language server mode, do not map syntax errors in order not to
		// complain about unfinished piece of code that user is currently writing.
		if strings.Contains(msg, "syntax error") && strings.Contains(msg, " at line ") && !LangServer {
			// it is in form "Syntax error: syntax error: unexpected '*' at line 4"
			if lastIdx := strings.LastIndexByte(msg, ' '); lastIdx > 0 {
				lineNumStr := msg[lastIdx+1:]
				lineNum, err := strconv.Atoi(lineNumStr)
				if err == nil {
					pos.StartLine = lineNum
					pos.EndLine = lineNum
					msg = msg[0:lastIdx]
					msg = strings.TrimSuffix(msg, " at line")
				}
			}
		}
	} else {
		pos = *n.GetPosition()
	}

	var endLn []byte
	var endChar int

	startLn, startChar := d.parseStartPos(&pos)

	if pos.EndLine >= 1 && len(d.Lines) > pos.EndLine {
		endLn = d.Lines[pos.EndLine-1]
		p := d.LinesPositions[pos.EndLine-1]
		if pos.EndPos > p {
			endChar = pos.EndPos - p
		}
	} else {
		endLn = startLn
	}

	if endChar == 0 {
		endChar = len(endLn)
	}

	if LangServer {
		severity, ok := vscodeLevelMap[level]
		if ok {
			diag := vscode.Diagnostic{
				Code:     msg,
				Message:  fmt.Sprintf(msg, args...),
				Severity: severity,
				Range: vscode.Range{
					Start: vscode.Position{Line: pos.StartLine - 1, Character: startChar},
					End:   vscode.Position{Line: pos.EndLine - 1, Character: endChar},
				},
			}

			if level == LevelUnused {
				diag.Tags = append(diag.Tags, 1 /* Unnecessary */)
			}

			d.Diagnostics = append(d.Diagnostics, diag)
		}
	} else {
		d.reports = append(d.reports, &Report{
			checkName:  checkName,
			startLn:    string(startLn),
			startChar:  startChar,
			startLine:  pos.StartLine,
			endChar:    endChar,
			level:      level,
			filename:   d.filename,
			msg:        fmt.Sprintf(msg, args...),
			isDisabled: d.disabledFlag,
		})
	}
}

func (d *RootWalker) reportUndefinedVariable(s *expr.Variable, maybeHave bool) {
	name, ok := s.VarName.(*node.Identifier)
	if !ok {
		d.Report(s, LevelInformation, "undefined", "Unknown variable variable %s used",
			meta.NameNodeToString(s))
		return
	}

	if _, ok := superGlobals[name.Value]; ok {
		return
	}

	if maybeHave {
		d.Report(s, LevelInformation, "undefined", "Variable might have not been defined: %s", name.Value)
	} else {
		d.Report(s, LevelError, "undefined", "Undefined variable: %s", name.Value)
	}
}

func (d *RootWalker) handleComment(c freefloating.String) {
	if c.StringType != freefloating.CommentType {
		return
	}
	str := c.Value

	if !phpdoc.IsPHPDoc(str) {
		return
	}

	for _, ln := range phpdoc.Parse(str) {
		if ln.Name != "linter" {
			continue
		}

		for _, p := range ln.Params {
			if p == "disable" {
				d.disabledFlag = true
			}
		}
	}
}

func (d *RootWalker) handleFuncStmts(params []meta.FuncParam, uses, stmts []node.Node, sc *meta.Scope) (returnTypes *meta.TypesMap, prematureExitFlags int) {
	b := &BlockWalker{
		ctx:          &blockContext{sc: sc},
		r:            d,
		unusedVars:   make(map[string][]node.Node),
		nonLocalVars: make(map[string]struct{}),
	}
	for _, createFn := range d.customBlock {
		b.custom = append(b.custom, createFn(&BlockContext{w: b}))
	}

	for _, useExpr := range uses {
		var byRef bool
		var v *expr.Variable
		switch u := useExpr.(type) {
		case *expr.Reference:
			v = u.Variable.(*expr.Variable)
			byRef = true
		case *expr.Variable:
			v = u
		}
		varName := v.VarName.(*node.Identifier).Value

		typ, ok := sc.GetVarNameType(varName)
		if !ok {
			typ = meta.NewTypesMap("TODO_use_var")
		}

		sc.AddVar(v, typ, "use", true)

		if !byRef {
			b.unusedVars[varName] = append(b.unusedVars[varName], v)
		} else {
			b.nonLocalVars[varName] = struct{}{}
		}
	}

	for _, p := range params {
		if p.IsRef {
			b.nonLocalVars[p.Name] = struct{}{}
		}
	}
	for _, s := range stmts {
		b.addStatement(s)
		s.Walk(b)
	}
	b.flushUnused()

	// we can mark function as exiting abnormally if and only if
	// it only exits with die; or throw; and does not exit
	// using return; or any other control structure
	cleanFlags := b.ctx.exitFlags & (FlagDie | FlagThrow)

	if b.ctx.exitFlags == cleanFlags && (b.ctx.containsExitFlags&FlagReturn) == 0 {
		prematureExitFlags = cleanFlags
	}

	return b.ctx.returnTypes, prematureExitFlags
}

func (d *RootWalker) getElementPos(n node.Node) meta.ElementPosition {
	pos := n.GetPosition()
	_, startChar := d.parseStartPos(pos)

	return meta.ElementPosition{
		Filename:  d.filename,
		Character: int32(startChar),
		Line:      int32(pos.StartLine),
		EndLine:   int32(pos.EndLine),
		Length:    int32(pos.EndPos - pos.StartPos),
	}
}

func (d *RootWalker) addScope(n node.Node, sc *meta.Scope) {
	if d.Scopes == nil {
		d.Scopes = make(map[node.Node]*meta.Scope)
	}
	d.Scopes[n] = sc
}

type methodModifiers struct {
	abstract    bool
	static      bool
	accessLevel meta.AccessLevel
	final       bool
}

func (d *RootWalker) parseMethodModifiers(meth *stmt.ClassMethod) (res methodModifiers) {
	res.accessLevel = meta.Public

	for _, m := range meth.Modifiers {
		switch v := m.(*node.Identifier).Value; v {
		case "abstract":
			res.abstract = true
		case "static":
			res.static = true
		case "public":
			res.accessLevel = meta.Public
		case "private":
			res.accessLevel = meta.Private
		case "protected":
			res.accessLevel = meta.Protected
		case "final":
			res.final = true
		default:
			linterError(d.filename, "Unrecognized method modifier: %s", v)
		}
	}

	return res
}

func (d *RootWalker) getClass() meta.ClassInfo {
	var m meta.ClassesMap

	if d.st.IsTrait {
		if d.meta.Traits == nil {
			d.meta.Traits = make(meta.ClassesMap)
		}
		m = d.meta.Traits
	} else {
		if d.meta.Classes == nil {
			d.meta.Classes = make(meta.ClassesMap)
		}
		m = d.meta.Classes
	}

	cl, ok := m[d.st.CurrentClass]
	if !ok {
		cl = meta.ClassInfo{
			Pos:              d.getElementPos(d.currentClassNode),
			Parent:           d.st.CurrentParentClass,
			ParentInterfaces: d.st.CurrentParentInterfaces,
			Interfaces:       make(map[string]struct{}),
			Traits:           make(map[string]struct{}),
			Methods:          make(meta.FunctionsMap),
			Properties:       make(meta.PropertiesMap),
			Constants:        make(meta.ConstantsMap),
		}

		m[d.st.CurrentClass] = cl
	}

	return cl
}

func (d *RootWalker) enterPropertyList(pl *stmt.PropertyList) bool {
	cl := d.getClass()

	isStatic := false
	accessLevel := meta.Public

	for _, m := range pl.Modifiers {
		switch m.(*node.Identifier).Value {
		case "public":
			accessLevel = meta.Public
		case "protected":
			accessLevel = meta.Protected
		case "private":
			accessLevel = meta.Private
		case "static":
			isStatic = true
		}
	}

	for _, pNode := range pl.Properties {
		p := pNode.(*stmt.Property)

		nm := p.Variable.(*expr.Variable).VarName.(*node.Identifier).Value

		typ, errText := d.parsePHPDocVar(p.PhpDocComment)
		if errText != "" {
			d.Report(p.Variable, LevelWarning, "phpdocLint", errText)
		}

		if p.Expr != nil {
			typ = typ.Append(solver.ExprTypeLocal(d.meta.Scope, d.st, p.Expr))
		}

		if isStatic {
			nm = "$" + nm
		}

		// TODO: handle duplicate property
		cl.Properties[nm] = meta.PropertyInfo{
			Pos:         d.getElementPos(p),
			Typ:         typ.Immutable(),
			AccessLevel: accessLevel,
		}
	}

	return true
}

func (d *RootWalker) enterClassConstList(s *stmt.ClassConstList) bool {
	cl := d.getClass()
	accessLevel := meta.Public

	for _, m := range s.Modifiers {
		switch m.(*node.Identifier).Value {
		case "public":
			accessLevel = meta.Public
		case "protected":
			accessLevel = meta.Protected
		case "private":
			accessLevel = meta.Private
		}
	}

	for _, cNode := range s.Consts {
		c := cNode.(*stmt.Constant)

		nm := c.ConstantName.(*node.Identifier).Value
		typ := solver.ExprTypeLocal(d.meta.Scope, d.st, c.Expr)

		// TODO: handle duplicate constant
		cl.Constants[nm] = meta.ConstantInfo{
			Pos:         d.getElementPos(c),
			Typ:         typ.Immutable(),
			AccessLevel: accessLevel,
		}
	}

	return true
}

func (d *RootWalker) checkOldStyleConstructor(meth *stmt.ClassMethod, nm string) {
	lastDelim := strings.IndexByte(d.st.CurrentClass, '\\')
	if strings.EqualFold(d.st.CurrentClass[lastDelim+1:], nm) {
		_, isClass := d.currentClassNode.(*stmt.Class)
		if isClass {
			d.Report(meth.MethodName, LevelDoNotReject, "oldStyleConstructor", "Old-style constructor usage, use __construct instead")
		}
	}
}

func (d *RootWalker) enterClassMethod(meth *stmt.ClassMethod) bool {
	nm := meth.MethodName.(*node.Identifier).Value

	d.checkOldStyleConstructor(meth, nm)

	pos := meth.GetPosition()

	if funcSize := pos.EndLine - pos.StartLine; funcSize > maxFunctionLines {
		d.Report(meth.MethodName, LevelDoNotReject, "complexity", "Too big method: more than %d lines", maxFunctionLines)
	}

	modif := d.parseMethodModifiers(meth)

	sc := meta.NewScope()
	if !modif.static {
		sc.AddVarName("this", meta.NewTypesMap(d.st.CurrentClass).Immutable(), "instance method", true)
		sc.SetInInstanceMethod(true)
	}

	var specifiedReturnType *meta.TypesMap
	if typ, ok := d.parseTypeNode(meth.ReturnType); ok {
		specifiedReturnType = typ
	}

	if meth.PhpDocComment == "" && modif.accessLevel == meta.Public {
		_, insideInterface := d.currentClassNode.(*stmt.Interface)
		// Permit having "__call" and other magic method without comments.
		if !insideInterface && !strings.HasPrefix(nm, "_") {
			d.Report(meth.MethodName, LevelDoNotReject, "phpdoc", "Missing PHPDoc for %q public method", nm)
		}
	}
	doc := d.parsePHPDoc(meth.PhpDocComment, meth.Params)
	d.reportPhpdocErrors(meth.MethodName, doc.errs)
	phpdocReturnType := doc.returnType
	phpDocParamTypes := doc.types

	class := d.getClass()
	params, minParamsCnt := d.parseFuncArgs(meth.Params, phpDocParamTypes, sc)

	if len(class.Interfaces) != 0 {
		// If we implement interfaces, methods that take a part in this
		// can borrow types information from them.
		// Programmers sometimes leave implementing methods without a
		// comment or use @inheritdoc there.
		//
		// If method params are properly documented, it's possible to
		// derive that information, but we need to know in which
		// interface we can find that method.
		//
		// Since we don't have all interfaces during the indexing phase
		// and shouldn't update meta after it, we defer type resolving by
		// using BaseMethodParam here. We would have to lookup
		// matching interface during the type resolving.

		// Find params without type and annotate them with special
		// type that will force solver to walk interface types that
		// current class implements to have a chance of finding relevant type info.
		for i, p := range params {
			if !p.Typ.IsEmpty() {
				continue // Already has a type
			}

			if i > math.MaxUint8 {
				break // Current implementation limit reached
			}

			res := make(map[string]struct{})
			res[meta.WrapBaseMethodParam(i, d.st.CurrentClass, nm)] = struct{}{}
			params[i].Typ = meta.NewTypesMapFromMap(res)
			sc.AddVarName(p.Name, params[i].Typ, "param", true)
		}
	}

	var stmts []node.Node
	if stmtList, ok := meth.Stmt.(*stmt.StmtList); ok {
		stmts = stmtList.Stmts
	}
	actualReturnTypes, exitFlags := d.handleFuncStmts(params, nil, stmts, sc)

	d.addScope(meth, sc)

	// TODO: handle duplicate method
	typ := meta.MergeTypeMaps(phpdocReturnType, actualReturnTypes, specifiedReturnType).Immutable()

	class.Methods[nm] = meta.FuncInfo{
		Params:       params,
		Pos:          d.getElementPos(meth),
		Typ:          typ,
		MinParamsCnt: minParamsCnt,
		AccessLevel:  modif.accessLevel,
		Static:       modif.static,
		ExitFlags:    exitFlags,
		Doc:          doc.info,
	}

	if nm == "getIterator" && meta.IsIndexingComplete() && solver.Implements(d.st.CurrentClass, `\IteratorAggregate`) {
		implementsTraversable := false
		typ.Iterate(func(typ string) {
			if implementsTraversable {
				return
			}

			if solver.Implements(typ, `\Traversable`) {
				implementsTraversable = true
			}
		})

		if !implementsTraversable {
			d.Report(meth.MethodName, LevelError, "stdInterface", "Objects returned by %s::getIterator() must be traversable or implement interface \\Iterator", d.st.CurrentClass)
		}
	}

	return false
}

type phpdocErrors struct {
	phpdocLint []string
	phpdocType []string
}

func (e *phpdocErrors) pushLint(format string, args ...interface{}) {
	e.phpdocLint = append(e.phpdocLint, fmt.Sprintf(format, args...))
}

func (e *phpdocErrors) pushType(format string, args ...interface{}) {
	e.phpdocType = append(e.phpdocType, fmt.Sprintf(format, args...))
}

type classPhpDocParseResult struct {
	properties meta.PropertiesMap
	errs       phpdocErrors
}

func (d *RootWalker) reportPhpdocErrors(n node.Node, errs phpdocErrors) {
	for _, err := range errs.phpdocLint {
		d.Report(n, LevelInformation, "phpdocLint", "%s", err)
	}
	for _, err := range errs.phpdocType {
		d.Report(n, LevelInformation, "phpdocType", "%s", err)
	}
}

func (d *RootWalker) parsePHPDocClass(doc string) classPhpDocParseResult {
	var result classPhpDocParseResult

	if doc == "" {
		return result
	}

	result.properties = make(meta.PropertiesMap)

	for _, part := range phpdoc.Parse(doc) {
		if part.Name != "property" {
			continue
		}

		// The syntax is:
		//	@property [Type] [name] [<description>]
		// Type and name are mandatory.

		if len(part.Params) < 2 {
			result.errs.pushLint("line %d: @property requires type and property name fields", part.Line)
			continue
		}

		typ := part.Params[0]
		name := part.Params[1]

		if err := d.checkPHPDocType(typ); err != "" {
			result.errs.pushType("%s on line %d", err, part.Line)
			continue
		}

		if !strings.HasPrefix(name, "$") {
			result.errs.pushLint("@property field name must start with `$`")
			continue
		}

		result.properties[name[len("$"):]] = meta.PropertyInfo{
			Typ:         meta.NewTypesMap(d.maybeAddNamespace(typ)),
			AccessLevel: meta.Public,
		}
	}

	return result
}

func (d *RootWalker) parsePHPDocVar(doc string) (m *meta.TypesMap, phpDocError string) {
	for _, part := range phpdoc.Parse(doc) {
		if part.Name == "var" && len(part.Params) >= 1 {
			m = meta.NewTypesMap(d.maybeAddNamespace(part.Params[0]))
		}
	}

	return m, phpDocError
}

func (d *RootWalker) maybeAddNamespace(typStr string) string {
	if typStr == "" {
		return ""
	}

	classNames := strings.Split(typStr, `|`)
	for idx, className := range classNames {
		// ignore things like \tuple(*)
		if braceIdx := strings.IndexByte(className, '('); braceIdx >= 0 {
			className = className[0:braceIdx]
		}

		// 0 for "bool", 1 for "bool[]", 2 for "bool[][]" and so on
		arrayDim := 0
		for strings.HasSuffix(className, "[]") {
			arrayDim++
			className = strings.TrimSuffix(className, "[]")
		}

		if len(className) == 0 {
			continue
		}

		switch className {
		case "bool", "boolean", "true", "false", "double", "float", "string", "int", "array", "resource", "mixed", "null", "callable", "void", "object":
			continue
		case "$this":
			// Handle `$this` as `static` alias in phpdoc context.
			classNames[idx] = "static"
			continue
		case "static":
			// Don't resolve `static` phpdoc type annotation too early
			// to make it possible to handle late static binding.
			continue
		}

		if className[0] == '\\' {
			continue
		}

		if className[0] <= meta.WMax {
			linterError(d.filename, "Bad type: '%s'", className)
			classNames[idx] = ""
			continue
		}

		fullClassName, ok := solver.GetClassName(d.st, meta.StringToName(className))
		if !ok {
			classNames[idx] = ""
			continue
		}

		if arrayDim > 0 {
			fullClassName += strings.Repeat("[]", arrayDim)
		}

		classNames[idx] = fullClassName
	}

	return strings.Join(classNames, "|")
}

func (d *RootWalker) checkPHPDocType(typ string) string {
	// TODO(quasilyte): might want to parse type and then check that none of
	// the type parts match that.

	// []T -> T[]
	if strings.HasPrefix(typ, "[]") {
		return fmt.Sprintf("%s type syntax: use [] after the type, e.g. T[]", typ)
	}

	// Check commonly mispelled types and other unfortunate cases.
	switch typ {
	case "boolean":
		return "use bool type instead of boolean"
	case "double", "real":
		return "use float type instead of " + typ
	case "long", "integer":
		return "use int type instead of " + typ
	case "-":
		// This happend when either of those formats is used:
		// `* @param $name - description`
		// `* @param - $name description`
		// We don't want to make "-" slip as a type name.
		return "expected a type, found '-'; if you want to express 'any' type, use 'mixed'"
	}

	return ""
}

type phpDocParseResult struct {
	returnType *meta.TypesMap
	types      phpDocParamsMap
	info       meta.PhpDocInfo
	errs       phpdocErrors
}

func (d *RootWalker) parsePHPDoc(doc string, actualParams []node.Node) phpDocParseResult {
	var result phpDocParseResult

	result.returnType = &meta.TypesMap{}

	if doc == "" {
		return result
	}

	result.types = make(phpDocParamsMap, len(actualParams))

	var curParam int

	for _, part := range phpdoc.Parse(doc) {
		if part.Name == "deprecated" {
			result.info.Deprecated = true
			result.info.DeprecationNote = part.ParamsText
			continue
		}

		if part.Name == "return" && len(part.Params) >= 1 {
			typ := part.Params[0]
			if err := d.checkPHPDocType(typ); err != "" {
				result.errs.pushType("%s on line %d", err, part.Line)
				continue
			}
			result.returnType = meta.NewTypesMap(d.maybeAddNamespace(typ))
			continue
		}

		// Rest is for @param handling.

		if part.Name != "param" || len(part.Params) < 1 {
			continue
		}

		typ := part.Params[0]
		optional := part.ContainsParam("[optional]")
		var variable string
		if len(part.Params) >= 2 {
			variable = part.Params[1]
		} else {
			// Either type of var name is missing.
			if strings.HasPrefix(typ, "$") {
				result.errs.pushLint("malformed @param %s tag (maybe type is missing?) on line %d",
					part.Params[0], part.Line)
				continue
			} else {
				result.errs.pushLint("malformed @param tag (maybe var is missing?) on line %d", part.Line)
			}
		}

		if len(part.Params) >= 2 && strings.HasPrefix(typ, "$") && !strings.HasPrefix(variable, "$") {
			// Phpstorm gives the same message.
			result.errs.pushLint("non-canonical order of variable and type on line %d", part.Line)
			variable, typ = typ, variable
		}

		if !strings.HasPrefix(variable, "$") {
			if len(actualParams) > curParam {
				variable = actualParams[curParam].(*node.Parameter).Variable.(*expr.Variable).VarName.(*node.Identifier).Value
			} else {
				result.errs.pushLint("too many @param tags on line %d", part.Line)
				continue
			}
		}

		curParam++

		var param phpDocParamEl
		if err := d.checkPHPDocType(typ); err != "" {
			result.errs.pushType("%s on line %d", err, part.Line)
		} else {
			param.typ = meta.NewTypesMap(d.maybeAddNamespace(typ))
		}
		param.optional = optional

		variable = strings.TrimPrefix(variable, "$")
		result.types[variable] = param
	}

	result.returnType = result.returnType.Immutable()
	return result
}

// parse type info, e.g. "string" in "someFunc() : string { ... }"
func (d *RootWalker) parseTypeNode(n node.Node) (typ *meta.TypesMap, ok bool) {
	if n == nil {
		return nil, false
	}

	switch t := n.(type) {
	case *name.Name:
		typ = meta.NewTypesMap(d.maybeAddNamespace(meta.NameToString(t)))
	case *name.FullyQualified:
		typ = meta.NewTypesMap(meta.FullyQualifiedToString(t))
	case *node.Identifier:
		typ = meta.NewTypesMap(t.Value)
	}

	return typ, typ != nil
}

func (d *RootWalker) parseFuncArgs(params []node.Node, parTypes phpDocParamsMap, sc *meta.Scope) (args []meta.FuncParam, minArgs int) {
	args = make([]meta.FuncParam, 0, len(params))
	for _, param := range params {
		p := param.(*node.Parameter)
		v := p.Variable.(*expr.Variable)
		parTyp := parTypes[v.VarName.(*node.Identifier).Value]

		if !parTyp.typ.IsEmpty() {
			sc.AddVar(v, parTyp.typ, "param", true)
		}

		typ := parTyp.typ

		if p.DefaultValue == nil && !parTyp.optional && !p.Variadic {
			minArgs++
		}

		if p.VariableType != nil {
			if varTyp, ok := d.parseTypeNode(p.VariableType); ok {
				typ = varTyp
			}
		} else if typ.IsEmpty() && p.DefaultValue != nil {
			typ = solver.ExprTypeLocal(sc, d.st, p.DefaultValue)
		}

		if p.Variadic {
			arrTyp := meta.NewEmptyTypesMap(typ.Len())
			typ.Iterate(func(t string) { arrTyp = arrTyp.AppendString(meta.WrapArrayOf(t)) })
			typ = arrTyp
		}

		sc.AddVar(v, typ, "param", true)

		par := meta.FuncParam{
			Typ:   typ.Immutable(),
			IsRef: p.ByRef,
		}

		if id, ok := v.VarName.(*node.Identifier); ok {
			par.Name = id.Value
		}

		args = append(args, par)
	}
	return args, minArgs
}

func (d *RootWalker) enterFunction(fun *stmt.Function) bool {
	nm := d.st.Namespace + `\` + fun.FunctionName.(*node.Identifier).Value
	pos := fun.GetPosition()

	if funcSize := pos.EndLine - pos.StartLine; funcSize > maxFunctionLines {
		d.Report(fun.FunctionName, LevelDoNotReject, "complexity", "Too big function: more than %d lines", maxFunctionLines)
	}

	var specifiedReturnType *meta.TypesMap
	if typ, ok := d.parseTypeNode(fun.ReturnType); ok {
		specifiedReturnType = typ
	}

	doc := d.parsePHPDoc(fun.PhpDocComment, fun.Params)
	d.reportPhpdocErrors(fun.FunctionName, doc.errs)
	phpdocReturnType := doc.returnType
	phpDocParamTypes := doc.types

	if d.meta.Functions == nil {
		d.meta.Functions = make(meta.FunctionsMap)
	}

	sc := meta.NewScope()

	params, minParamsCnt := d.parseFuncArgs(fun.Params, phpDocParamTypes, sc)

	actualReturnTypes, exitFlags := d.handleFuncStmts(params, nil, fun.Stmts, sc)
	d.addScope(fun, sc)

	d.meta.Functions[nm] = meta.FuncInfo{
		Params:       params,
		Pos:          d.getElementPos(fun),
		Typ:          meta.MergeTypeMaps(phpdocReturnType, actualReturnTypes, specifiedReturnType).Immutable(),
		MinParamsCnt: minParamsCnt,
		ExitFlags:    exitFlags,
		Doc:          doc.info,
	}

	return false
}

func (d *RootWalker) enterFunctionCall(s *expr.FunctionCall) bool {
	nm, ok := s.Function.(*name.Name)
	if !ok {
		return true
	}

	if d.st.Namespace == `\PHPSTORM_META` && meta.NameEquals(nm, `override`) {
		return d.handleOverride(s)
	}

	if !meta.NameEquals(nm, `define`) || len(s.ArgumentList.Arguments) < 2 {
		// TODO: actually we could warn about bogus defines
		return true
	}

	arg, ok := s.ArgumentList.Arguments[0].(*node.Argument)
	if !ok {
		return true
	}

	str, ok := arg.Expr.(*scalar.String)
	if !ok {
		return true
	}

	valueArg, ok := s.ArgumentList.Arguments[1].(*node.Argument)
	if !ok {
		return true
	}

	if d.meta.Constants == nil {
		d.meta.Constants = make(meta.ConstantsMap)
	}

	d.meta.Constants[`\`+strings.TrimFunc(str.Value, isQuote)] = meta.ConstantInfo{
		Pos: d.getElementPos(s),
		Typ: solver.ExprTypeLocal(d.meta.Scope, d.st, valueArg.Expr),
	}
	return true
}

// Handle e.g. "override(\array_shift(0), elementType(0));"
// which means "return type of array_shift() is the type of element of first function parameter"
func (d *RootWalker) handleOverride(s *expr.FunctionCall) bool {
	if len(s.ArgumentList.Arguments) != 2 {
		return true
	}

	arg0, ok := s.ArgumentList.Arguments[0].(*node.Argument)
	if !ok {
		return true
	}

	arg1, ok := s.ArgumentList.Arguments[1].(*node.Argument)
	if !ok {
		return true
	}

	fc0, ok := arg0.Expr.(*expr.FunctionCall)
	if !ok {
		return true
	}

	fc1, ok := arg1.Expr.(*expr.FunctionCall)
	if !ok {
		return true
	}

	fnNameNode, ok := fc0.Function.(*name.FullyQualified)
	if !ok {
		return true
	}

	overrideNameNode, ok := fc1.Function.(*name.Name)
	if !ok {
		return true
	}

	if len(fc1.ArgumentList.Arguments) != 1 {
		return true
	}

	fc1Arg0, ok := fc1.ArgumentList.Arguments[0].(*node.Argument)
	if !ok {
		return true
	}

	argNumNode, ok := fc1Arg0.Expr.(*scalar.Lnumber)
	if !ok {
		return true
	}

	argNum, err := strconv.Atoi(argNumNode.Value)
	if err != nil {
		return true
	}

	var overrideTyp meta.OverrideType
	switch {
	case meta.NameEquals(overrideNameNode, `type`):
		overrideTyp = meta.OverrideArgType
	case meta.NameEquals(overrideNameNode, `elementType`):
		overrideTyp = meta.OverrideElementType
	default:
		return true
	}

	fnName := meta.FullyQualifiedToString(fnNameNode)

	if d.meta.FunctionOverrides == nil {
		d.meta.FunctionOverrides = make(meta.FunctionsOverrideMap)
	}

	d.meta.FunctionOverrides[fnName] = meta.FuncInfoOverride{
		OverrideType: overrideTyp,
		ArgNum:       argNum,
	}

	return true
}

func (d *RootWalker) enterConstList(lst *stmt.ConstList) bool {
	if d.meta.Constants == nil {
		d.meta.Constants = make(meta.ConstantsMap)
	}

	for _, sNode := range lst.Consts {
		s := sNode.(*stmt.Constant)

		id, ok := s.ConstantName.(*node.Identifier)
		if !ok {
			continue
		}

		nm := d.st.Namespace + `\` + id.Value

		d.meta.Constants[nm] = meta.ConstantInfo{
			Pos: d.getElementPos(s),
			Typ: solver.ExprTypeLocal(d.meta.Scope, d.st, s.Expr),
		}
	}

	return false
}

// GetChildrenVisitor is invoked at every node parameter that contains children nodes
func (d *RootWalker) GetChildrenVisitor(key string) walker.Visitor {
	return d
}

// LeaveNode is invoked after node process
func (d *RootWalker) LeaveNode(n walker.Walkable) {
	for _, c := range d.custom {
		c.BeforeLeaveNode(n)
	}

	switch n.(type) {
	case *stmt.Class, *stmt.Interface, *stmt.Trait:
		d.getClass() // populate classes map

		d.currentClassNode = nil
	}

	state.LeaveNode(d.st, n)

	for _, c := range d.custom {
		c.AfterLeaveNode(n)
	}
}
