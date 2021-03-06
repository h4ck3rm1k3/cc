package parse

import (
	"fmt"
	"github.com/andrewchambers/cc/cpp"
	"os"
	"runtime/debug"
	"strconv"
)

// Storage class
type SClass int

const (
	SC_AUTO SClass = iota
	SC_REGISTER
	SC_STATIC
	SC_TYPEDEF
	SC_GLOBAL
)

type parseErrorBreakOut struct {
	err error
}

type gotoFixup struct {
	actualLabel string
	g           *Goto
}

type parser struct {
	types   *scope
	structs *scope
	decls   *scope

	pp          *cpp.Preprocessor
	curt, nextt *cpp.Token
	lcounter    int

	breakCounter int
	breaks       [2048]string
	contCounter  int
	continues    [2048]string

	switchCounter int
	switchs       [2048]*Switch

	// Map of goto labels to anonymous labels.
	labels map[string]string
	// All gotos found in the current function.
	// Needed so we can fix up forward references.
	gotos []gotoFixup
}

func (p *parser) pushScope() {
	p.decls = newScope(p.decls)
	p.structs = newScope(p.structs)
	p.types = newScope(p.types)
}

func (p *parser) popScope() {
	p.decls = p.decls.parent
	p.structs = p.structs.parent
	p.types = p.types.parent
}

func (p *parser) pushSwitch(s *Switch) {
	p.switchs[p.switchCounter] = s
	p.switchCounter += 1
}

func (p *parser) popSwitch() {
	p.switchCounter -= 1
}

func (p *parser) getSwitch() *Switch {
	if p.switchCounter == 0 {
		return nil
	}
	return p.switchs[p.switchCounter-1]
}

func (p *parser) pushBreak(blabel string) {
	p.breaks[p.breakCounter] = blabel
	p.breakCounter += 1
}

func (p *parser) pushCont(clabel string) {
	p.continues[p.contCounter] = clabel
	p.contCounter += 1
}

func (p *parser) popBreak() {
	p.breakCounter -= 1
	if p.breakCounter < 0 {
		panic("internal error")
	}
}

func (p *parser) popCont() {
	p.contCounter -= 1
	if p.contCounter < 0 {
		panic("internal error")
	}
}

func (p *parser) pushBreakCont(blabel, clabel string) {
	p.pushBreak(blabel)
	p.pushCont(clabel)
}

func (p *parser) popBreakCont() {
	p.popBreak()
	p.popCont()
}

func (p *parser) getBreakLabel() string {
	if p.breakCounter == 0 {
		return ""
	}
	return p.breaks[p.breakCounter-1]
}

func (p *parser) getContLabel() string {
	if p.contCounter == 0 {
		return ""
	}
	return p.continues[p.contCounter-1]
}

func (p *parser) nextLabel() string {
	p.lcounter += 1
	return fmt.Sprintf(".L%d", p.lcounter)
}

func Parse(pp *cpp.Preprocessor) (toplevels []Node, errRet error) {
	p := &parser{}
	p.pp = pp
	p.types = newScope(nil)
	p.decls = newScope(nil)
	p.structs = newScope(nil)

	defer func() {
		if e := recover(); e != nil {
			peb := e.(parseErrorBreakOut) // Will re-panic if not a breakout.
			errRet = peb.err
		}
	}()
	p.next()
	p.next()
	toplevels = p.parseTranslationUnit()
	return toplevels, nil
}

func (p *parser) errorPos(pos cpp.FilePos, m string, vals ...interface{}) {
	err := fmt.Errorf(m, vals...)
	if os.Getenv("CCDEBUG") == "true" {
		err = fmt.Errorf("%s\n%s", err, debug.Stack())
	}
	err = cpp.ErrWithLoc(err, pos)
	panic(parseErrorBreakOut{err})
}

func (p *parser) error(m string, vals ...interface{}) {
	err := fmt.Errorf(m, vals...)
	if os.Getenv("CCDEBUG") == "true" {
		err = fmt.Errorf("%s\n%s", err, debug.Stack())
	}
	panic(parseErrorBreakOut{err})
}

func (p *parser) expect(k cpp.TokenKind) {
	if p.curt.Kind != k {
		p.errorPos(p.curt.Pos, "expected %s got %s", k, p.curt.Kind)
	}
	p.next()
}

func (p *parser) next() {
	p.curt = p.nextt
	t, err := p.pp.Next()
	if err != nil {
		p.error(err.Error())
	}
	p.nextt = t
}

func (p *parser) ensureScalar(n Expr) {
	if !IsScalarType(n.GetType()) {
		p.errorPos(n.GetPos(), "expected scalar type")
	}
}

func (p *parser) parseTranslationUnit() []Node {
	var topLevels []Node
	for p.curt.Kind != cpp.EOF {
		toplevel := p.parseDecl(true)
		topLevels = append(topLevels, toplevel)
	}
	return topLevels
}

func isDeclStart(t cpp.TokenKind) bool {
	switch t {
	case cpp.STATIC, cpp.VOLATILE, cpp.STRUCT, cpp.CHAR, cpp.INT, cpp.SHORT, cpp.LONG,
		cpp.UNSIGNED, cpp.SIGNED, cpp.FLOAT, cpp.DOUBLE:
		return true
	}
	return false
}

func (p *parser) parseStmt() Node {
	if p.nextt.Kind == ':' && p.curt.Kind == cpp.IDENT {
		return p.parseLabeledStmt()
	}
	if isDeclStart(p.curt.Kind) {
		return p.parseDecl(false)
	} else {
		switch p.curt.Kind {
		case cpp.CASE:
			return p.parseCase()
		case cpp.DEFAULT:
			return p.parseDefault()
		case cpp.GOTO:
			return p.parseGoto()
		case ';':
			pos := p.curt.Pos
			p.next()
			return &EmptyStmt{
				Pos: pos,
			}
		case cpp.SWITCH:
			return p.parseSwitch()
		case cpp.RETURN:
			return p.parseReturn()
		case cpp.WHILE:
			return p.parseWhile()
		case cpp.DO:
			return p.parseDoWhile()
		case cpp.FOR:
			return p.parseFor()
		case cpp.BREAK, cpp.CONTINUE:
			return p.parseBreakCont()
		case cpp.IF:
			return p.parseIf()
		case '{':
			return p.parseBlock()
		default:
			pos := p.curt.Pos
			expr := p.parseExpr()
			p.expect(';')
			return &ExprStmt{
				Pos:  pos,
				Expr: expr,
			}
		}
	}
	panic("unreachable.")
}

func (p *parser) parseSwitch() Node {
	sw := &Switch{}
	sw.Pos = p.curt.Pos
	sw.LAfter = p.nextLabel()
	p.expect(cpp.SWITCH)
	p.expect('(')
	expr := p.parseExpr()
	sw.Expr = expr
	if !IsIntType(expr.GetType()) {
		p.errorPos(expr.GetPos(), "switch expression expects an integral type")
	}
	p.expect(')')
	p.pushSwitch(sw)
	p.pushBreak(sw.LAfter)
	stmt := p.parseStmt()
	sw.Stmt = stmt
	p.popBreak()
	p.popSwitch()
	return sw
}

func (p *parser) parseGoto() Node {
	pos := p.curt.Pos
	p.next()
	actualLabel := p.curt.Val
	p.expect(cpp.IDENT)
	p.expect(';')
	ret := &Goto{
		Pos:   pos,
		Label: "", // To be fixed later.
	}
	p.gotos = append(p.gotos, gotoFixup{
		actualLabel,
		ret,
	})
	return ret
}

func (p *parser) parseLabeledStmt() Node {
	pos := p.curt.Pos
	label := p.curt.Val
	anonlabel := p.nextLabel()
	_, ok := p.labels[label]
	if ok {
		p.errorPos(pos, "redefinition of label %s in function", label)
	}
	p.labels[label] = anonlabel
	p.expect(cpp.IDENT)
	p.expect(':')
	return &LabeledStmt{
		Pos:       pos,
		Label:     label,
		AnonLabel: anonlabel,
		Stmt:      p.parseStmt(),
	}
}

func (p *parser) parseCase() Node {
	pos := p.curt.Pos
	p.expect(cpp.CASE)
	sw := p.getSwitch()
	if sw == nil {
		p.errorPos(pos, "'case' outside a switch statement")
	}
	expr := p.parseExpr()
	if !IsIntType(expr.GetType()) {
		p.errorPos(expr.GetPos(), "expected an integral type")
	}
	v, err := Fold(expr)
	if err != nil {
		p.errorPos(expr.GetPos(), err.Error())
	}
	p.expect(':')
	anonlabel := p.nextLabel()
	i := v.(*ConstantInt)
	swc := SwitchCase{
		V:     i.Val,
		Label: anonlabel,
	}
	sw.Cases = append(sw.Cases, swc)
	return &LabeledStmt{
		Pos:       pos,
		AnonLabel: anonlabel,
		Stmt:      p.parseStmt(),
		IsCase:    true,
	}
}

func (p *parser) parseDefault() Node {
	pos := p.curt.Pos
	p.expect(cpp.DEFAULT)
	sw := p.getSwitch()
	if sw == nil {
		p.errorPos(pos, "'default' outside a switch statement")
	}
	p.expect(':')
	if sw.LDefault != "" {
		p.errorPos(pos, "multiple default statements in switch")
	}
	anonlabel := p.nextLabel()
	sw.LDefault = anonlabel
	return &LabeledStmt{
		Pos:       pos,
		AnonLabel: anonlabel,
		Stmt:      p.parseStmt(),
		IsDefault: true,
	}
}

func (p *parser) parseBreakCont() Node {
	pos := p.curt.Pos
	label := ""
	isbreak := p.curt.Kind == cpp.BREAK
	iscont := p.curt.Kind == cpp.CONTINUE
	if isbreak {
		label = p.getBreakLabel()
		if label == "" {
			p.errorPos(pos, "break outside of loop/switch")
		}
	}
	if iscont {
		label = p.getContLabel()
		if label == "" {
			p.errorPos(pos, "continue outside of loop/switch")
		}
	}
	p.next()
	p.expect(';')
	return &Goto{
		Pos:     pos,
		IsBreak: isbreak,
		IsCont:  iscont,
		Label:   label,
	}
}

func (p *parser) parseReturn() Node {
	pos := p.curt.Pos
	p.expect(cpp.RETURN)
	expr := p.parseExpr()
	p.expect(';')
	return &Return{
		Pos: pos,
		Ret: expr,
	}
}

func (p *parser) parseIf() Node {
	ifpos := p.curt.Pos
	lelse := p.nextLabel()
	p.expect(cpp.IF)
	p.expect('(')
	expr := p.parseExpr()
	p.ensureScalar(expr)
	p.expect(')')
	stmt := p.parseStmt()
	var els Node
	if p.curt.Kind == cpp.ELSE {
		p.next()
		els = p.parseStmt()
	}
	return &If{
		Pos:   ifpos,
		Cond:  expr,
		Stmt:  stmt,
		Else:  els,
		LElse: lelse,
	}
}

func (p *parser) parseFor() Node {
	pos := p.curt.Pos
	lstart := p.nextLabel()
	lend := p.nextLabel()
	var init, cond, step Expr
	p.expect(cpp.FOR)
	p.expect('(')
	if p.curt.Kind != ';' {
		init = p.parseExpr()
	}
	p.expect(';')
	if p.curt.Kind != ';' {
		cond = p.parseExpr()
	}
	p.expect(';')
	if p.curt.Kind != ')' {
		step = p.parseExpr()
	}
	p.expect(')')
	p.pushBreakCont(lend, lstart)
	body := p.parseStmt()
	p.popBreakCont()
	return &For{
		Pos:    pos,
		Init:   init,
		Cond:   cond,
		Step:   step,
		Body:   body,
		LStart: lstart,
		LEnd:   lend,
	}
}

func (p *parser) parseWhile() Node {
	pos := p.curt.Pos
	lstart := p.nextLabel()
	lend := p.nextLabel()
	p.expect(cpp.WHILE)
	p.expect('(')
	cond := p.parseExpr()
	p.ensureScalar(cond)
	p.expect(')')
	p.pushBreakCont(lend, lstart)
	body := p.parseStmt()
	p.popBreakCont()
	return &While{
		Pos:    pos,
		Cond:   cond,
		Body:   body,
		LStart: lstart,
		LEnd:   lend,
	}
}

func (p *parser) parseDoWhile() Node {
	pos := p.curt.Pos
	lstart := p.nextLabel()
	lcond := p.nextLabel()
	lend := p.nextLabel()
	p.expect(cpp.DO)
	p.pushBreakCont(lend, lcond)
	body := p.parseStmt()
	p.popBreakCont()
	p.expect(cpp.WHILE)
	p.expect('(')
	cond := p.parseExpr()
	p.expect(')')
	p.expect(';')
	return &DoWhile{
		Pos:    pos,
		Body:   body,
		Cond:   cond,
		LStart: lstart,
		LCond:  lcond,
		LEnd:   lend,
	}
}

func (p *parser) parseBlock() *CompndStmt {
	var stmts []Node
	pos := p.curt.Pos
	p.expect('{')
	for p.curt.Kind != '}' {
		stmts = append(stmts, p.parseStmt())
	}
	p.expect('}')
	return &CompndStmt{
		Pos:  pos,
		Body: stmts,
	}
}

func (p *parser) parseFuncBody(f *Function) {
	p.labels = make(map[string]string)
	p.gotos = nil
	for p.curt.Kind != '}' {
		stmt := p.parseStmt()
		f.Body = append(f.Body, stmt)
	}
	for _, fixup := range p.gotos {
		anonlabel, ok := p.labels[fixup.actualLabel]
		if !ok {
			p.errorPos(fixup.g.GetPos(), "goto target %s is undefined", fixup.actualLabel)
		}
		fixup.g.Label = anonlabel
	}
}

func (p *parser) parseDecl(isGlobal bool) Node {
	firstDecl := true
	declPos := p.curt.Pos
	var name *cpp.Token
	declList := &DeclList{}
	sc, ty := p.parseDeclSpecifiers()
	declList.Storage = sc
	isTypedef := sc == SC_TYPEDEF
	for {
		name, ty = p.parseDeclarator(ty, false)
		if name == nil {
			panic("internal error")
		}
		if firstDecl && isGlobal {
			// if declaring a function
			if p.curt.Kind == '{' {
				if isTypedef {
					p.errorPos(name.Pos, "cannot typedef a function")
				}
				fty, ok := ty.(*FunctionType)
				if !ok {
					p.errorPos(name.Pos, "expected a function")
				}
				err := p.decls.define(name.Val, &GSymbol{
					Label: name.Val,
					Type:  fty,
				})
				if err != nil {
					p.errorPos(declPos, err.Error())
				}
				p.pushScope()
				var psyms []*LSymbol

				for idx, name := range fty.ArgNames {
					sym := &LSymbol{
						Type: fty.ArgTypes[idx],
					}
					psyms = append(psyms, sym)
					err := p.decls.define(name, sym)
					if err != nil {
						p.errorPos(declPos, "multiple params with name %s", name)
					}
				}
				f := &Function{
					Name:         name.Val,
					FuncType:     fty,
					Pos:          declPos,
					ParamSymbols: psyms,
				}
				p.expect('{')
				p.parseFuncBody(f)
				p.expect('}')
				p.popScope()
				return f
			}
		}
		var sym Symbol
		if isTypedef {
			sym = &TSymbol{
				Type: ty,
			}
		} else if isGlobal {
			sym = &GSymbol{
				Label: name.Val,
				Type:  ty,
			}
		} else {
			sym = &LSymbol{
				Type: ty,
			}
		}
		var err error
		if isTypedef {
			err = p.types.define(name.Val, sym)
		} else {
			err = p.decls.define(name.Val, sym)
		}
		if err != nil {
			p.errorPos(name.Pos, err.Error())
		}
		declList.Symbols = append(declList.Symbols, sym)
		var init Node
		var initPos cpp.FilePos
		var folded ConstantValue
		if p.curt.Kind == '=' {
			p.next()
			initPos = p.curt.Pos
			if isTypedef {
				p.errorPos(initPos, "cannot initialize a typedef")
			}
			init = p.parseInitializer(nil, true)
			folded, err = Fold(init)
			if err != nil {
				folded = nil
				if isGlobal {
					p.errorPos(initPos, err.Error())
				}
			}
		}
		declList.Inits = append(declList.Inits, init)
		declList.FoldedInits = append(declList.FoldedInits, folded)
		if p.curt.Kind != ',' {
			break
		}
		p.next()
		firstDecl = false
	}
	if p.curt.Kind != ';' {
		p.errorPos(p.curt.Pos, "expected '=', ',' or ';'")
	}
	p.expect(';')
	return declList
}

func (p *parser) parseParamDecl() (*cpp.Token, CType) {
	_, ty := p.parseDeclSpecifiers()
	return p.parseDeclarator(ty, true)
}

func isStorageClass(k cpp.TokenKind) (bool, SClass) {
	switch k {
	case cpp.STATIC:
		return true, SC_STATIC
	case cpp.EXTERN:
		return true, SC_GLOBAL
	case cpp.TYPEDEF:
		return true, SC_TYPEDEF
	case cpp.REGISTER:
		return true, SC_REGISTER
	}
	return false, 0
}

type dSpec struct {
	signedcnt   int
	unsignedcnt int
	charcnt     int
	intcnt      int
	shortcnt    int
	longcnt     int
	floatcnt    int
	doublecnt   int
}

type dSpecLutEnt struct {
	spec dSpec
	ty   Primitive
}

var declSpecLut = [...]dSpecLutEnt{
	dSpecLutEnt{dSpec{
		charcnt: 1,
	}, CChar},
	dSpecLutEnt{dSpec{
		signedcnt: 1,
		charcnt:   1,
	}, CChar},
	dSpecLutEnt{dSpec{
		unsignedcnt: 1,
		charcnt:     1,
	}, CUChar},
	dSpecLutEnt{dSpec{
		shortcnt: 1,
	}, CShort},
	dSpecLutEnt{dSpec{
		signedcnt: 1,
		shortcnt:  1,
	}, CShort},
	dSpecLutEnt{dSpec{
		intcnt:   1,
		shortcnt: 1,
	}, CShort},
	dSpecLutEnt{dSpec{
		signedcnt: 1,
		intcnt:    1,
		shortcnt:  1,
	}, CShort},
	dSpecLutEnt{dSpec{
		signedcnt: 1,
		intcnt:    1,
		shortcnt:  1,
	}, CShort},
	dSpecLutEnt{dSpec{
		unsignedcnt: 1,
		intcnt:      1,
		shortcnt:    1,
	}, CUShort},
	dSpecLutEnt{dSpec{
		unsignedcnt: 1,
		shortcnt:    1,
	}, CUShort},
	dSpecLutEnt{dSpec{
		intcnt: 1,
	}, CInt},
	dSpecLutEnt{dSpec{
		intcnt: 1,
	}, CInt},
	dSpecLutEnt{dSpec{
		signedcnt: 1,
	}, CInt},
	dSpecLutEnt{dSpec{
		signedcnt: 1,
		intcnt:    1,
	}, CInt},
	dSpecLutEnt{dSpec{
		unsignedcnt: 1,
	}, CUInt},
	dSpecLutEnt{dSpec{
		unsignedcnt: 1,
		intcnt:      1,
	}, CUInt},
	dSpecLutEnt{dSpec{
		longcnt: 1,
	}, CLong},
	dSpecLutEnt{dSpec{
		signedcnt: 1,
		longcnt:   1,
	}, CLong},
	dSpecLutEnt{dSpec{
		longcnt: 1,
		intcnt:  1,
	}, CLong},
	dSpecLutEnt{dSpec{
		signedcnt: 1,
		longcnt:   1,
		intcnt:    1,
	}, CLong},
	dSpecLutEnt{dSpec{
		unsignedcnt: 1,
		longcnt:     1,
	}, CULong},
	dSpecLutEnt{dSpec{
		unsignedcnt: 1,
		longcnt:     1,
		intcnt:      1,
	}, CLong},
	dSpecLutEnt{dSpec{
		longcnt: 2,
	}, CLLong},
	dSpecLutEnt{dSpec{
		signedcnt: 1,
		longcnt:   2,
	}, CLLong},
	dSpecLutEnt{dSpec{
		intcnt:  1,
		longcnt: 2,
	}, CLLong},
	dSpecLutEnt{dSpec{
		intcnt:    1,
		signedcnt: 1,
		longcnt:   2,
	}, CLLong},
	dSpecLutEnt{dSpec{
		unsignedcnt: 1,
		longcnt:     2,
	}, CULLong},
	dSpecLutEnt{dSpec{
		intcnt:      1,
		unsignedcnt: 1,
		longcnt:     2,
	}, CULLong},
	dSpecLutEnt{dSpec{
		floatcnt: 1,
	}, CFloat},
	dSpecLutEnt{dSpec{
		doublecnt: 1,
	}, CDouble},
}

func (p *parser) parseDeclSpecifiers() (SClass, CType) {
	dspecpos := p.curt.Pos
	scassigned := false
	sc := SC_AUTO
	var ty CType = CInt
	var spec dSpec
	nullspec := dSpec{}
loop:
	for {
		pos := p.curt.Pos
		issc, sclass := isStorageClass(p.curt.Kind)
		if issc {
			if scassigned {
				p.errorPos(pos, "only one storage class specifier allowed")
			}
			scassigned = true
			sc = sclass
			p.next()
			continue
		}
		switch p.curt.Kind {
		case cpp.VOID:
			p.next()
		case cpp.CHAR:
			spec.charcnt += 1
			p.next()
		case cpp.SHORT:
			spec.shortcnt += 1
			p.next()
		case cpp.INT:
			spec.intcnt += 1
			p.next()
		case cpp.LONG:
			spec.longcnt += 1
			p.next()
		case cpp.FLOAT:
			spec.floatcnt += 1
			p.next()
		case cpp.DOUBLE:
			spec.doublecnt += 1
			p.next()
		case cpp.SIGNED:
			spec.signedcnt += 1
			p.next()
		case cpp.UNSIGNED:
			spec.unsignedcnt += 1
			p.next()
		case cpp.IDENT:
			t := p.curt
			sym, err := p.types.lookup(t.Val)
			if err != nil {
				break loop
			}
			tsym := sym.(*TSymbol)
			p.next()
			if spec != nullspec {
				p.error("TODO...")
			}
			return sc, tsym.Type
		case cpp.STRUCT:
			p.parseStruct()
			return sc, ty
		case cpp.UNION:
		case cpp.VOLATILE, cpp.CONST:
			p.next()
		default:
			break loop
		}
	}

	// If we got any type specifiers, look up
	// the correct type.
	if spec != nullspec {
		match := false
		for _, te := range declSpecLut {
			if te.spec == spec {
				ty = te.ty
				match = true
				break
			}
		}
		if !match {
			p.errorPos(dspecpos, "invalid type")
		}
	}
	return sc, ty
}

// Declarator
// ----------
//
// A declarator is the part of a Decl that specifies
// the name that is to be introduced into the program.
//
// unsigned int a, *b, **c, *const*d *volatile*e ;
//              ^  ^^  ^^^  ^^^^^^^^ ^^^^^^^^^^^
//
// Direct Declarator
// -----------------
//
// A direct declarator is missing the pointer prefix.
//
// e.g.
// unsigned int *a[32], b[];
//               ^^^^^  ^^^
//
// Abstract Declarator
// -------------------
//
// A delcarator missing an identifier.

func (p *parser) parseDeclarator(basety CType, abstract bool) (*cpp.Token, CType) {
	for p.curt.Kind == cpp.CONST || p.curt.Kind == cpp.VOLATILE {
		p.next()
	}
	switch p.curt.Kind {
	case '*':
		p.next()
		name, ty := p.parseDeclarator(basety, abstract)
		return name, &Ptr{ty}
	case '(':
		forward := &ForwardedType{}
		p.next()
		name, ty := p.parseDeclarator(forward, abstract)
		p.expect(')')
		forward.Type = p.parseDeclaratorTail(basety)
		return name, ty
	case cpp.IDENT:
		name := p.curt
		p.next()
		return name, p.parseDeclaratorTail(basety)
	default:
		if abstract {
			return nil, p.parseDeclaratorTail(basety)
		}
		p.errorPos(p.curt.Pos, "expected ident, '(' or '*' but got %s", p.curt.Kind)
	}
	panic("unreachable")
}

func (p *parser) parseDeclaratorTail(basety CType) CType {
	ret := basety
	for {
		switch p.curt.Kind {
		case '[':
			p.next()
			var dimn Node
			if p.curt.Kind != ']' {
				dimn = p.parseAssignmentExpr()
			}
			p.expect(']')
			dim, err := Fold(dimn)
			if err != nil {
				p.errorPos(dimn.GetPos(), "invalid constant Expr for array dimensions")
			}
			i := dim.(*ConstantInt)
			ret = &Array{
				Dim:        int(i.Val),
				MemberType: ret,
			}
		case '(':
			fret := &FunctionType{}
			fret.RetType = basety
			p.next()
			if p.curt.Kind != ')' {
				for {
					pnametok, pty := p.parseParamDecl()
					pname := ""
					if pnametok != nil {
						pname = pnametok.Val
					}
					fret.ArgTypes = append(fret.ArgTypes, pty)
					fret.ArgNames = append(fret.ArgNames, pname)
					if p.curt.Kind == ',' {
						p.next()
						continue
					}
					break
				}
			}
			p.expect(')')
			ret = fret
		default:
			return ret
		}
	}
}

func (p *parser) parseInitializer(ty CType, constant bool) Node {
	return p.parseAssignmentExpr()
	/*
		_ = p.curt.Pos
		if IsScalarType(ty) {
			var init Expr
			if p.curt.Kind == '{' {
				p.expect('{')
				init = p.parseAssignmentExpr()
				p.expect('}')
			} else {
				init = p.parseAssignmentExpr()
			}
			return init
		} else if IsCharArr(ty) {
			switch p.curt.Kind {
			case cpp.STRING:
				p.expect(cpp.STRING)
			case '{':
				p.expect('{')
				p.expect(cpp.STRING)
				p.expect('}')
			default:
			}
		} else if IsArrType(ty) {
			arr := ty.(*Array)
			p.expect('{')
			var inits []Node
			for p.curt.Kind != '}' {
				inits = append(inits, p.parseInitializer(arr.MemberType, true))
				if p.curt.Kind == ',' {
					continue
				}
			}
			p.expect('}')
		}
	*/
	return nil
}

func isAssignmentOperator(k cpp.TokenKind) bool {
	switch k {
	case '=', cpp.ADD_ASSIGN, cpp.SUB_ASSIGN, cpp.MUL_ASSIGN, cpp.QUO_ASSIGN, cpp.REM_ASSIGN,
		cpp.AND_ASSIGN, cpp.OR_ASSIGN, cpp.XOR_ASSIGN, cpp.SHL_ASSIGN, cpp.SHR_ASSIGN:
		return true
	}
	return false
}

func (p *parser) parseExpr() Expr {
	var ret Expr
	for {
		ret = p.parseAssignmentExpr()
		if p.curt.Kind != ',' {
			break
		}
		p.next()
	}
	return ret
}

func (p *parser) parseAssignmentExpr() Expr {
	l := p.parseCondExpr()
	if isAssignmentOperator(p.curt.Kind) {
		pos := p.curt.Pos
		op := p.curt.Kind
		p.next()
		r := p.parseAssignmentExpr()
		l = &Binop{
			Pos: pos,
			Op:  op,
			L:   l,
			R:   r,
		}
	}
	return l
}

// Aka Ternary operator.
func (p *parser) parseCondExpr() Expr {
	return p.parseLogOrExpr()
}

func (p *parser) parseLogOrExpr() Expr {
	l := p.parseLogAndExpr()
	for p.curt.Kind == cpp.LOR {
		pos := p.curt.Pos
		op := p.curt.Kind
		p.next()
		r := p.parseLogAndExpr()
		l = &Binop{
			Pos: pos,
			Op:  op,
			L:   l,
			R:   r,
		}
	}
	return l
}

func (p *parser) parseLogAndExpr() Expr {
	l := p.parseInclusiveOrExpr()
	for p.curt.Kind == cpp.LAND {
		pos := p.curt.Pos
		op := p.curt.Kind
		p.next()
		r := p.parseInclusiveOrExpr()
		l = &Binop{
			Pos: pos,
			Op:  op,
			L:   l,
			R:   r,
		}
	}
	return l
}

func (p *parser) parseInclusiveOrExpr() Expr {
	l := p.parseExclusiveOrExpr()
	for p.curt.Kind == '|' {
		pos := p.curt.Pos
		op := p.curt.Kind
		p.next()
		r := p.parseExclusiveOrExpr()
		l = &Binop{
			Pos: pos,
			Op:  op,
			L:   l,
			R:   r,
		}
	}
	return l
}

func (p *parser) parseExclusiveOrExpr() Expr {
	l := p.parseAndExpr()
	for p.curt.Kind == '^' {
		pos := p.curt.Pos
		op := p.curt.Kind
		p.next()
		r := p.parseAndExpr()
		l = &Binop{
			Pos: pos,
			Op:  op,
			L:   l,
			R:   r,
		}
	}
	return l
}

func (p *parser) parseAndExpr() Expr {
	l := p.parseEqualityExpr()
	for p.curt.Kind == '&' {
		pos := p.curt.Pos
		op := p.curt.Kind
		p.next()
		r := p.parseEqualityExpr()
		l = &Binop{
			Pos: pos,
			Op:  op,
			L:   l,
			R:   r,
		}
	}
	return l
}

func (p *parser) parseEqualityExpr() Expr {
	l := p.parseRelationalExpr()
	for p.curt.Kind == cpp.EQL || p.curt.Kind == cpp.NEQ {
		pos := p.curt.Pos
		op := p.curt.Kind
		p.next()
		r := p.parseRelationalExpr()
		l = &Binop{
			Pos:  pos,
			Op:   op,
			L:    l,
			R:    r,
			Type: CInt,
		}
	}
	return l
}

func (p *parser) parseRelationalExpr() Expr {
	l := p.parseShiftExpr()
	for p.curt.Kind == '>' || p.curt.Kind == '<' || p.curt.Kind == cpp.LEQ || p.curt.Kind == cpp.GEQ {
		pos := p.curt.Pos
		op := p.curt.Kind
		p.next()
		r := p.parseShiftExpr()
		l = &Binop{
			Pos:  pos,
			Op:   op,
			L:    l,
			R:    r,
			Type: CInt,
		}
	}
	return l
}

func (p *parser) parseShiftExpr() Expr {
	l := p.parseAdditiveExpr()
	for p.curt.Kind == cpp.SHL || p.curt.Kind == cpp.SHR {
		pos := p.curt.Pos
		op := p.curt.Kind
		p.next()
		r := p.parseAdditiveExpr()
		l = &Binop{
			Pos: pos,
			Op:  op,
			L:   l,
			R:   r,
		}
	}
	return l
}

func (p *parser) parseAdditiveExpr() Expr {
	l := p.parseMultiplicativeExpr()
	for p.curt.Kind == '+' || p.curt.Kind == '-' {
		pos := p.curt.Pos
		op := p.curt.Kind
		p.next()
		r := p.parseMultiplicativeExpr()
		l = &Binop{
			Pos:  pos,
			Op:   op,
			L:    l,
			R:    r,
			Type: CInt,
		}
	}
	return l
}

func (p *parser) parseMultiplicativeExpr() Expr {
	l := p.parseCastExpr()
	for p.curt.Kind == '*' || p.curt.Kind == '/' || p.curt.Kind == '%' {
		pos := p.curt.Pos
		op := p.curt.Kind
		p.next()
		r := p.parseCastExpr()
		l = &Binop{
			Pos:  pos,
			Op:   op,
			L:    l,
			R:    r,
			Type: CInt,
		}
	}
	return l
}

func (p *parser) parseCastExpr() Expr {
	// Cast
	return p.parseUnaryExpr()
}

func (p *parser) parseUnaryExpr() Expr {
	switch p.curt.Kind {
	case cpp.INC, cpp.DEC:
		p.next()
		p.parseUnaryExpr()
	case '*', '+', '-', '!', '~', '&':
		pos := p.curt.Pos
		op := p.curt.Kind
		p.next()
		operand := p.parseCastExpr()
		ty := operand.GetType()
		if op == '&' {
			ty = &Ptr{
				PointsTo: ty,
			}
		} else if op == '*' {
			ptr, ok := ty.(*Ptr)
			if !ok {
				p.errorPos(pos, "dereferencing requires a pointer type")
			}
			ty = ptr.PointsTo
		}
		return &Unop{
			Pos:     pos,
			Op:      op,
			Operand: operand,
			Type:    ty,
		}
	default:
		return p.parsePostfixExpr()
	}
	panic("unreachable")
}

func (p *parser) parsePostfixExpr() Expr {
	l := p.parsePrimaryExpr()
loop:
	for {
		switch p.curt.Kind {
		case '[':
			var ty CType
			arr, isArr := l.GetType().(*Array)
			ptr, isPtr := l.GetType().(*Ptr)
			if !isArr && !isPtr {
				p.errorPos(p.curt.Pos, "Can only index into array or pointer types")
			}
			if isArr {
				ty = arr.MemberType
			}
			if isPtr {
				ty = ptr.PointsTo
			}
			p.next()
			idx := p.parseExpr()
			p.expect(']')
			l = &Index{
				Arr:  l,
				Idx:  idx,
				Type: ty,
			}
		case '.', cpp.ARROW:
			p.next()
			// XXX is a typename valid here too?
			p.expect(cpp.IDENT)
		case '(':
			parenpos := p.curt.Pos
			var fty *FunctionType
			switch ty := l.GetType().(type) {
			case *Ptr:
				functy, ok := ty.PointsTo.(*FunctionType)
				if !ok {
					p.errorPos(l.GetPos(), "expected a function pointer")
				}
				fty = functy
			case *FunctionType:
				fty = ty
			default:
				p.errorPos(l.GetPos(), "expected a func or func pointer")
			}
			var args []Expr
			p.next()
			if p.curt.Kind != ')' {
				for {
					args = append(args, p.parseAssignmentExpr())
					if p.curt.Kind == ',' {
						p.next()
						continue
					}
					break
				}
			}
			p.expect(')')
			return &Call{
				Pos:      parenpos,
				FuncLike: l,
				Args:     args,
				Type:     fty.RetType,
			}
		case cpp.INC:
			p.next()
		case cpp.DEC:
			p.next()
		default:
			break loop
		}
	}
	return l
}

func constantToExpr(t *cpp.Token) (Expr, error) {
	switch t.Kind {
	case cpp.INT_CONSTANT:
		v, err := strconv.ParseInt(t.Val, 0, 64)
		return &Constant{
			Val:  v,
			Pos:  t.Pos,
			Type: CInt,
		}, err
	default:
		return nil, fmt.Errorf("internal error - %s", t.Kind)
	}
}

func (p *parser) parsePrimaryExpr() Expr {
	switch p.curt.Kind {
	case cpp.IDENT:
		sym, err := p.decls.lookup(p.curt.Val)
		if err != nil {
			p.errorPos(p.curt.Pos, "undefined symbol %s", p.curt.Val)
		}
		p.next()
		return &Ident{
			Sym: sym,
		}
	case cpp.INT_CONSTANT:
		t := p.curt
		p.next()
		n, err := constantToExpr(t)
		if err != nil {
			p.errorPos(t.Pos, err.Error())
		}
		return n
	case cpp.CHAR_CONSTANT:
		p.next()
	case cpp.STRING:
		s := p.curt
		p.next()
		return &String{
			Pos:   s.Pos,
			Val:   s.Val,
			Label: p.nextLabel(),
		}
	case '(':
		p.next()
		p.parseExpr()
		p.expect(')')
	default:
		p.errorPos(p.curt.Pos, "expected an identifier, constant, string or Expr")
	}
	panic("unreachable")
}

func (p *parser) parseStruct() CType {
	p.expect(cpp.STRUCT)
	if p.curt.Kind == cpp.IDENT {
		p.next()
	}
	if p.curt.Kind == '{' {
		p.next()
		for {
			if p.curt.Kind == '}' {
				break
			}
			_, basety := p.parseDeclSpecifiers()
			for {
				p.parseDeclarator(basety, false)
				if p.curt.Kind == ',' {
					p.next()
					continue
				}
				break
			}
			p.expect(';')
		}
		p.expect('}')
	}
	return nil
}
