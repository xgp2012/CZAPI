import axios from 'axios'
import { ElMessage } from 'element-plus'
import router from './router'

// 统一的后端客户端。
//
// 会话靠 HttpOnly Cookie，前端拿不到也存不了 token，因此这里不需要
// 处理 Authorization 头——这本身就是防 XSS 窃取会话的设计。
const http = axios.create({
  baseURL: '/api/v10/console',
  timeout: 20000,
  // Cookie 由浏览器自动带，但必须显式允许
  withCredentials: true,
})

http.interceptors.response.use(
  (resp) => {
    const body = resp.data
    // 后端信封：{success, message, data}
    if (body && body.success === false) {
      ElMessage.error(body.message || '操作失败')
      return Promise.reject(new Error(body.message || '操作失败'))
    }
    return body?.data ?? body
  },
  (error) => {
    const status = error.response?.status
    const msg = error.response?.data?.message || error.message || '网络错误'

    if (status === 401) {
      // 会话失效：回登录页，并带上来路以便登录后跳回
      const cur = router.currentRoute.value
      if (cur.name !== 'login') {
        router.replace({ name: 'login', query: { redirect: cur.fullPath } })
      }
      ElMessage.warning('登录已失效，请重新登录')
      return Promise.reject(error)
    }

    // 403 且提示改密：强制跳改密页
    if (status === 403 && String(msg).includes('密码')) {
      router.replace({ name: 'password' })
      ElMessage.warning(msg)
      return Promise.reject(error)
    }

    ElMessage.error(msg)
    return Promise.reject(error)
  }
)

export default http
