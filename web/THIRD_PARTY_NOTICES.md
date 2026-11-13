# 前端第三方许可与参考

界面以 AIHOT 的主题层次、导航外壳、阅读信息流和小趋势图为视觉与交互参考，固定上游提交为 `269765738daf6b15316fa69e504087a73f54738b`。本项目自行实现 React 组件、数据绑定、主题样式和品牌图标。 [AIHOT 源码与许可证](https://github.com/KKKKhazix/AIHOT/tree/269765738daf6b15316fa69e504087a73f54738b)

- 主题层次参考 `apps/web/app/app.css` 的亮暗色语义。
- 导航参考 `apps/web/app/components/shell`。
- 信息流参考 `apps/web/app/features/feed`。
- 小趋势图参考 `apps/web/app/features/hot/Sparkline.tsx` 的缺测断线表现。
- 主题色值来自 Radix Colors，图标使用 Lucide。 [Radix Colors](https://github.com/radix-ui/colors) [Lucide](https://github.com/lucide-icons/lucide)
- Star 详情图使用 ECharts。自托管的 DM Sans 和 IBM Plex Mono 字体使用 OFL 许可。 [ECharts](https://github.com/apache/echarts) [Fontsource](https://github.com/fontsource/fontsource)
- 导航反馈和详情操作参考 AIHOT 的 `Chrome.tsx`、`Tabs.tsx` 与 `features/item`，组件使用本项目的数据和样式重新实现。分享海报在浏览器生成二维码，使用无运行时依赖的 uqr。 [AIHOT 参考提交](https://github.com/KKKKhazix/AIHOT/tree/83b32d3f168940c8e796d1be2468197900292db1) [uqr](https://github.com/unjs/uqr)
- 导出 Markdown 时使用 Turndown 转换站内已渲染的 README。 [Turndown](https://github.com/mixmark-io/turndown)

实际依赖版本由 `package-lock.json` 固定。构建会把运行时库和字体的原始许可与声明写到 `dist/third-party-licenses.txt`，可在部署站点的 `/third-party-licenses.txt` 读取。
