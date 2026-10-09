package funccode

// The starter code of each runtime: a handler that answers ?name=Ada with
// {"hello":"Ada"}. A copy of the dashboard's - its function-templates.constants.ts -
// so that a function begun here is the one begun there: a change of one is made
// in the other.
//
//nolint:lll,goconst // each file as it is, one string
var templates = map[string][]File{
	Node24: {
		{Path: "package.json", Content: "{\n  \"type\": \"module\"\n}\n"},
		{Path: "index.js", Content: "// A function answers one request: it returns { status, headers, body }.\n// A body that is an object is sent as JSON.\nexport default async function (req, ctx) {\n    ctx.log(`${req.method} ${req.path}`);\n    const name = req.query.name ?? \"world\";\n\n    return { status: 200, body: { hello: name } };\n}\n"},
	},
	Bun1: {
		{Path: "package.json", Content: "{\n  \"type\": \"module\"\n}\n"},
		{Path: "index.ts", Content: "// A function answers one request: it returns { status, headers, body }.\n// A body that is an object is sent as JSON. Bun runs TypeScript whole; its\n// types are not checked.\ninterface Request {\n    method: string;\n    path: string;\n    query: Record<string, string | undefined>;\n    queryAll: Record<string, string[] | undefined>;\n    headers: Record<string, string | undefined>;\n    body: Buffer;\n    text(): string;\n    json(): unknown;\n}\n\ninterface Context {\n    requestId: string;\n    deadline: number;\n    signal: AbortSignal;\n    log(...args: unknown[]): void;\n}\n\ninterface Response {\n    status?: number;\n    headers?: Record<string, string | string[]>;\n    body?: unknown;\n}\n\nexport default async function (req: Request, ctx: Context): Promise<Response> {\n    ctx.log(`${req.method} ${req.path}`);\n    const name = req.query.name ?? \"world\";\n\n    return { status: 200, body: { hello: name } };\n}\n"},
	},
	Python313: {
		{Path: "main.py", Content: "# A function answers one request: it returns a dict of status, headers, body.\n# A body that is a dict is sent as JSON. An async def handler is the fastest,\n# but must not block: blocking code (time.sleep, requests) belongs in a def.\ndef handler(req, ctx):\n    ctx.log(f\"{req.method} {req.path}\")\n    name = req.query.get(\"name\", \"world\")\n\n    return {\"status\": 200, \"body\": {\"hello\": name}}\n"},
	},
	Go127: {
		{Path: "go.mod", Content: "module example.com/function\n\ngo 1.27\n"},
		{Path: "handler.go", Content: "package function\n\nimport (\n\t\"context\"\n\n\t\"github.com/hivepaas/function-runtimes/hivepaas\"\n)\n\n// Handle answers one request.\nfunc Handle(ctx context.Context, req *hivepaas.Request) (*hivepaas.Response, error) {\n\thivepaas.Log(ctx, \"%s %s\", req.Method, req.Path)\n\tname := req.Query.Get(\"name\")\n\tif name == \"\" {\n\t\tname = \"world\"\n\t}\n\n\treturn hivepaas.JSON(200, map[string]string{\"hello\": name})\n}\n"},
	},
}

// typeScriptTemplate is Node.js's in TypeScript: Node.js removes the types as
// it loads the file.
//
//nolint:lll,goconst // each file as it is, one string
var typeScriptTemplate = []File{
	{Path: "package.json", Content: "{\n  \"type\": \"module\"\n}\n"},
	{Path: "index.ts", Content: "// A function answers one request: it returns { status, headers, body }.\n// A body that is an object is sent as JSON. Node.js runs this file by\n// removing its types: they are not checked, and syntax that cannot be\n// removed - enum, namespace - is not allowed.\ninterface Request {\n    method: string;\n    path: string;\n    query: Record<string, string | undefined>;\n    queryAll: Record<string, string[] | undefined>;\n    headers: Record<string, string | undefined>;\n    body: Buffer;\n    text(): string;\n    json(): unknown;\n}\n\ninterface Context {\n    requestId: string;\n    deadline: number;\n    signal: AbortSignal;\n    log(...args: unknown[]): void;\n}\n\ninterface Response {\n    status?: number;\n    headers?: Record<string, string | string[]>;\n    body?: unknown;\n}\n\nexport default async function (req: Request, ctx: Context): Promise<Response> {\n    ctx.log(`${req.method} ${req.path}`);\n    const name = req.query.name ?? \"world\";\n\n    return { status: 200, body: { hello: name } };\n}\n"},
}
