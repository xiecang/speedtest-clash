// Generated from cvt filter.ts; see UPSTREAM.md.
class AND {
    left;
    right;
    constructor(left, right){
        this.left = left;
        this.right = right;
    }
    eval(o) {
        return this.left.eval(o) && this.right.eval(o);
    }
}
class OR {
    left;
    right;
    constructor(left, right){
        this.left = left;
        this.right = right;
    }
    eval(o) {
        return this.left.eval(o) || this.right.eval(o);
    }
}
class NOT {
    child;
    constructor(child){
        this.child = child;
    }
    eval(o) {
        return !this.child.eval(o);
    }
}
class CMP {
    path;
    eq;
    re;
    constructor(path, eq, re){
        this.path = path;
        this.eq = eq;
        this.re = re;
    }
    eval(o) {
        return cmp(o, this.path, this.eq, this.re);
    }
}
function cmp(o, path, eq, re, vis = new Set()) {
    for(let i = 0; i < path.length; i++){
        const p = path[i];
        if (typeof o !== 'object' || !o) return !eq;
        if (!Array.isArray(o)) {
            if (p === 'length') o = Object.keys(o).length;
            else {
                o = o[p];
            }
            continue;
        }
        if (typeof p === 'number') o = o[p < 0 ? o.length + p : p];
        else if (p === 'length') o = o.length;
        else {
            path = path.slice(i);
            return o.some((x)=>cmp(x, path, eq, re, vis));
        }
    }
    if (o == null) return !eq;
    if (typeof o !== 'object') return re.test(String(o)) === eq;
    if (vis.has(o)) return false;
    vis.add(o);
    const arr = Array.isArray(o) ? o : Object.values(o);
    return arr.some((x)=>cmp(x, [], eq, re, vis));
}
function parsePathAndOp(expr, i) {
    const path = [];
    const re = /(?:([\w-]+|\$)|\[\s*(?:(["'])((?:\\.|.)*?)\2|(-?\d+))\s*\])\s*(?:([=:]|![=:]?)|\.|(?=\[))\s*/gy;
    re.lastIndex = i;
    for (const m of expr.matchAll(re)){
        if (m[1] !== '$') {
            path.push(m[1] ?? m[3]?.replace(/\\(\\)?/g, '$1') ?? Number(m[4]));
        }
        if (m[5]) {
            return {
                path,
                eq: !m[5].startsWith('!'),
                exact: m[5].endsWith('='),
                i: m.index + m[0].length
            };
        }
    }
    if (path.length === 0) {
        const reOp = /([=:]|![=:]?)\s*/y;
        reOp.lastIndex = i;
        const m = reOp.exec(expr);
        if (m) return {
            path: [
                'name'
            ],
            eq: !m[1].startsWith('!'),
            exact: m[1].endsWith('='),
            i: m.index + m[0].length
        };
    }
    return {
        path: [
            'name'
        ],
        eq: true,
        exact: false,
        i
    };
}
function parseRe(expr, exact, i) {
    let start = i;
    let paren = 0;
    let inCharClass = false;
    for(; i < expr.length; i++){
        const c = expr[i];
        if (c === '\\') {
            i++;
            if (i >= expr.length) break;
            continue;
        }
        if (inCharClass) {
            if (c === ']') inCharClass = false;
            continue;
        }
        if (c === '[') {
            inCharClass = true;
            continue;
        }
        if (c === '(') {
            paren++;
            continue;
        }
        if (c === ')') {
            if (paren === 0) {
                if (expr[start - 1] === '(') {
                    start--;
                    continue;
                }
                break;
            }
            paren--;
            continue;
        }
        if (paren === 0 && /\s/.test(c)) break;
    }
    if (start === i) return undefined;
    const pattern = expr.slice(start, i);
    return {
        re: exact ? new RegExp(`^(?:${pattern})$`, 'u') : new RegExp(pattern, 'iu'),
        start,
        end: i
    };
}
function parseCmp(expr, i, toks) {
    const { path, eq, exact, i: reStart } = parsePathAndOp(expr, i);
    const re = parseRe(expr, exact, reStart);
    if (!re) return undefined;
    const deleteCount = reStart - re.start;
    toks.splice(toks.length - deleteCount, deleteCount, {
        type: 'CMP',
        path,
        eq,
        re: re.re
    });
    const reSpaces = /\s*/y;
    reSpaces.lastIndex = re.end;
    return re.end + (reSpaces.exec(expr)?.[0].length ?? 0);
}
function parseLogics(expr, i, toks) {
    const re = /(?:([()])|(and|or|not)\s)\s*/igy;
    re.lastIndex = i;
    for (const m of expr.matchAll(re)){
        toks.push({
            type: m[1] ?? m[2].toUpperCase()
        });
        i = m.index + m[0].length;
    }
    return i;
}
function tokenize(expr) {
    expr = expr.trim();
    const toks = [];
    let i = 0;
    while(i < expr.length){
        i = parseLogics(expr, i, toks);
        if (i >= expr.length) break;
        const j = parseCmp(expr, i, toks);
        if (j === undefined) {
            throw new SyntaxError(`Unexpected token at pos ${i}: \`${expr.slice(i, i + 10)}\``);
        }
        i = j;
    }
    toks.push({
        type: 'EOF'
    });
    return toks;
}
class Parser {
    static parse(expr) {
        const parser = new Parser(expr);
        const node = parser.parseOr();
        if (parser.curType() !== 'EOF') throw new SyntaxError('Extra input after end of expression');
        return node;
    }
    tokens;
    pos = 0;
    constructor(expr){
        this.tokens = tokenize(expr);
    }
    curType() {
        return this.tokens[this.pos].type;
    }
    accept(expected) {
        const t = this.tokens[this.pos];
        if (t.type !== expected) return undefined;
        this.pos++;
        return t;
    }
    consume(expected) {
        const t = this.tokens[this.pos];
        if (t.type !== expected) throw new SyntaxError(`Expected ${expected}, got ${t.type}`);
        this.pos++;
        return t;
    }
    parseOr() {
        let node = this.parseAnd();
        while(this.accept('OR')){
            node = new OR(node, this.parseAnd());
        }
        return node;
    }
    parseAnd() {
        let node = this.parseNot();
        while(this.accept('AND')){
            node = new AND(node, this.parseNot());
        }
        return node;
    }
    parseNot() {
        if (this.accept('NOT')) return new NOT(this.parseNot());
        return this.parsePrimary();
    }
    parsePrimary() {
        const cmp = this.accept('CMP');
        if (cmp) return new CMP(cmp.path, cmp.eq, cmp.re);
        if (this.accept('(')) {
            const node = this.parseOr();
            this.consume(')');
            return node;
        }
        throw new SyntaxError(`Unexpected token ${this.curType()}`);
    }
}
class Filter {
    ast;
    constructor(expr){
        this.ast = Parser.parse(expr);
    }
    test(o) {
        return this.ast.eval(o);
    }
}

const filters = new Map();
function compileFilter(expr) {
  if (!filters.has(expr)) {
    if (filters.size >= 256) filters.clear();
    filters.set(expr, new Filter(expr));
  }
  return filters.get(expr);
}
function matchFilter(expr, nodes) {
  const filter = compileFilter(expr);
  return JSON.stringify(nodes.map(node => filter.test(node)));
}
