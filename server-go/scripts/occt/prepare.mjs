// Prepares occt-import-js's WebAssembly (OpenCascade, as Node's model.ts
// loads it) for internal/stepmesh, which runs it under wazero:
//
//   - the legacy exception-handling instructions (try, catch, rethrow) are
//     rewritten as exnref ones (try_table, throw_ref), the form wazero runs;
//   - one exported function per embind invoker signature calls a table
//     entry (call_indirect), since wazero's API cannot call by table index;
//   - a custom section, studio.occt, names the minified imports and exports
//     as the JavaScript glue beside the module (occt-import-js.js) names
//     them, and the package version.
//
// The module is otherwise unchanged: the same OpenCascade code meshes the
// same file. A second module, <out>.noeh.wasm, has the exception handling
// stripped (a throw becomes a trap): wazero copies the whole stack on every
// try block entered, which OpenCascade enters constantly, so a file is
// meshed with that module first and with the full one only when it traps.
// Without a throw the two run the same code. Usage:
//   node prepare.mjs <binaryen index.js> <occt-import-js.wasm> <out.wasm>
import fs from 'node:fs'
import path from 'node:path'
import { pathToFileURL } from 'node:url'

const [, , binaryenArg, inArg, outArg] = process.argv
if (!outArg) {
  console.error('usage: node prepare.mjs <binaryen index.js> <occt-import-js.wasm> <out.wasm>')
  process.exit(2)
}
const { default: binaryen } = await import(pathToFileURL(binaryenArg).href)
const F = binaryen.Features
const features = F.MVP | F.ExceptionHandling | F.ReferenceTypes | F.BulkMemory | F.SignExt
  | F.MutableGlobals | F.NontrappingFPToInt | F.Multivalue

// The glue's names for the module's imports (var wasmImports={a:___x,...})
// and exports (_malloc=wasmExports["ca"]).
const glue = fs.readFileSync(inArg.replace(/\.wasm$/, '.js'), 'utf8')
const imp = /var wasmImports=\{([^}]*)\}/.exec(glue)
if (!imp) throw new Error('no wasmImports in the glue')
const imports = Object.fromEntries(imp[1].split(',').map(e => e.split(':')))
const exports = {}
for (const m of glue.matchAll(/([A-Za-z_$][\w$]*)=wasmExports\["([^"]+)"\]/g)) exports[m[1]] = m[2]
const pkg = JSON.parse(fs.readFileSync(path.join(path.dirname(inArg), '..', 'package.json'), 'utf8'))
const section = new TextEncoder().encode(JSON.stringify({ package: `${pkg.name}@${pkg.version}`, imports, exports }))

function build(passes, out) {
  const mod = binaryen.readBinary(fs.readFileSync(inArg))
  mod.setFeatures(features)
  mod.runPasses(passes)
  // Trampolines: __studio_call_<sig>(index, args...) calls table 0 at
  // index. Signatures are embind's: the result, then the arguments; p (a
  // pointer on wasm32) is i32.
  const table = binaryen.getTableInfo(mod.getTableByIndex(0)).name
  for (const sig of ['pppp', 'ppppp']) {
    const args = [...sig.slice(1)].map(() => binaryen.i32)
    const params = binaryen.createType([binaryen.i32, ...args])
    const operands = args.map((ty, i) => mod.local.get(i + 1, ty))
    const name = `__studio_call_${sig}`
    mod.addFunction(name, params, binaryen.i32, [],
      mod.call_indirect(table, mod.local.get(0, binaryen.i32), operands, binaryen.createType(args), binaryen.i32))
    mod.addFunctionExport(name, name)
  }
  mod.addCustomSection('studio.occt', section)
  if (!mod.validate()) throw new Error(`${out}: the prepared module does not validate`)
  fs.writeFileSync(out, mod.emitBinary())
  mod.dispose()
}

build(['translate-to-exnref'], outArg)
build(['strip-eh'], outArg.replace(/\.wasm$/, '') + '.noeh.wasm')
