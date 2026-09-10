# T004 教学笔记：前端脚手架（Vite + React + TS）

> 对应文件：`web/{package.json, vite.config.ts, tsconfig.json, index.html, src/*}`
> 一句话：不请脚手架生成器，手工搭一间"最小但严格"的前端样板间——5 个运行时依赖
> 一个组件库都没有，但 TypeScript 严格模式全开。

## 1. 为什么手工搭而不用 `npm create vite`？

任务要求"最小依赖"。生成器模板自带：ESLint、Prettier、多个 tsconfig 变体、示例 CSS……
对 M1 三页面的工作台是行李冗余。手工写 7 个文件，**每一个文件为什么存在都答得上来**，
也顺便把脚手架生成器替你藏起来的知识摊开（这正是教学任务的目的）。

## 2. package.json：依赖的"体重"意识

```json
"dependencies": { "react": "^19.0.0", "react-dom": "^19.0.0" },
"devDependencies": { "vite", "@vitejs/plugin-react", "typescript", "@types/react", "@types/react-dom" }
```

- 运行时依赖只有 react/react-dom 两个——**没有路由库、没有状态库、没有组件库**。
  M1 三个页面用最朴素的 useState + fetch 就够（research.md §6 决策）；需要时再引
  TanStack Query，而不是预先打包。
- `^19.0.0` 的插入符（caret）语义：允许 19.x 内升级，禁止跳到 20——npm 生态用
  语义化版本 + 插入符做"自动小步升级"，与 Go 的模块版本选择（MVS）哲学不同：
  npm 允许依赖树里同名库多版本共存，Go 全仓只允许一个版本。

## 3. vite.config.ts：开发期代理的三行魔术

```ts
server: {
  proxy: { '/api': { target: 'http://127.0.0.1:8080', changeOrigin: true } }
}
```

浏览器直接 fetch `http://127.0.0.1:8080` 会撞**同源策略**（CORS）：前端在 5173 端口，
Go 在 8080，跨端口即跨源。两条路：

1. Go 侧加 CORS 中间件（改服务端，生产也要背这个逻辑）；
2. **Vite 开发服务器代理**（改开发环境）：前端永远请求同源的 `/api/...`，Vite 在
   服务端转发给 8080——浏览器的跨源问题根本不发生。

选 2：生产部署时前端静态文件与 API 同域（或走反向代理），CORS 问题不存在；
代理只在开发期存在。前端代码里写相对路径 `/api/...`，**环境差异被吸收在配置层**。

## 4. tsconfig.json：严格模式是"编译期的 code review"

```json
"strict": true, "noUnusedLocals": true, "noUnusedParameters": true,
"verbatimModuleSyntax": true, "moduleResolution": "bundler"
```

- **strict**：一开关带十个子开关（null 检查、this 类型、初始化检查……）。M1 的 UI
  是审批入口，`draft.body` 一个 undefined 就可能渲染成"批准了一条空评论"——
  null 安全不是洁癖，是业务安全。
- **verbatimModuleSyntax**：强制 `import type { Foo }` 显式区分类型导入，编译产物
  干净（类型导入直接擦除），也避免"这个 import 是类型还是值"的猜谜。
- **moduleResolution: "bundler"**：告诉 TS"你面对的是 Vite 这类打包器"，按打包器
  规则解析导入（允许无扩展名、导出映射），是 Vite 时代的标准搭配。
- **noEmit**：TS 只做类型检查，产出代码的事交给 Vite（esbuild/rollup）——各司其职。

`npm run build` 里的 `tsc -b && vite build` 顺序有意为之：**先类型检查，后打包**，
类型错误阻止产物生成——把 TS 当类型关卡，不是当转译器。

## 5. index.html 与入口：单页应用的"洞"

```html
<div id="root"></div>
<script type="module" src="/src/main.tsx"></script>
```

整个 React 应用被 `createRoot(...).render(...)` 注入这个空 div——HTML 只是个"洞"，
一切 UI 由 JS 生成。`type="module"` 触发浏览器原生 ESM 加载，开发期 Vite 按**请求粒度**
即时编译单个模块（这就是 Vite 冷启动快的秘密：不做整体打包）。

`document.getElementById('root')!` 的 `!` 是 TS 的非空断言：我们**向编译器担保**这个
id 一定存在。合理的用法场景（同仓库内的静态模板，肉眼可证），但值得记住这是断言不是校验。

## 6. 自测证据

| 场景 | 结果 |
| --- | --- |
| `npm install` | 69 packages，0 脆弱依赖警告阻塞 ✅ |
| `npm run build`（tsc 严格检查 + vite 打包） | 28 modules，842ms，无类型错误 ✅ |
| dev server + 代理链路 | ⏳ 留待 Phase 2 Checkpoint 与 Go API 联调验证 |

## 7. 下一站预告

T027/T028 会把 `App.tsx` 的占位换成三个真页面（仓库接入、Case 列表、任务详情+审批）。
脚手架的纪律（相对路径 API、严格类型）到时候直接兑现红利。
