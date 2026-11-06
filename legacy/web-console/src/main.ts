import { createApp } from 'vue'
import { createPinia } from 'pinia'
import ElementPlus from 'element-plus'
import zhCn from 'element-plus/es/locale/lang/zh-cn'

// Order matters: tokens define the variables, element-theme maps EP onto them,
// global.css holds the shared primitives and utilities.
import '@/styles/tokens.css'
import 'element-plus/dist/index.css'
import '@/styles/element-theme.css'
import '@/styles/global.css'

import App from './App.vue'
import { router } from './router'
import { applyTheme, theme } from './utils/theme'

applyTheme(theme.value)

const app = createApp(App)
app.use(createPinia())
app.use(router)
app.use(ElementPlus, { locale: zhCn, size: 'default', zIndex: 2000 })
app.mount('#app')
