<template>
  <div class="cz-login-wrap">
    <div class="cz-login-card">
      <h1 class="cz-login-title">CZAPI 管理后台</h1>
      <p class="cz-login-sub">LLM 网关运维控制台</p>

      <el-form :model="form" @submit.prevent="submit">
        <el-form-item>
          <el-input v-model="form.username" placeholder="用户名" size="large" autofocus />
        </el-form-item>
        <el-form-item>
          <el-input
            v-model="form.password"
            type="password"
            placeholder="密码"
            size="large"
            show-password
            @keyup.enter="submit"
          />
        </el-form-item>
        <el-button
          type="primary"
          size="large"
          style="width: 100%"
          :loading="loading"
          @click="submit"
        >
          登录
        </el-button>
      </el-form>

      <p class="cz-hint">
        首次使用默认账号 admin / admin，登录后会要求立即修改密码。<br />
        客户无需登录此处，请从业务系统进入。
      </p>
    </div>
  </div>
</template>

<script setup>
import { reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import http from '../api'

const router = useRouter()
const route = useRoute()

const form = reactive({ username: '', password: '' })
const loading = ref(false)

async function submit() {
  if (!form.username || !form.password) {
    ElMessage.warning('请填写用户名和密码')
    return
  }

  loading.value = true
  try {
    const me = await http.post('/login', form)
    if (me.must_change_password) {
      ElMessage.warning('请先修改默认密码')
      router.replace({ name: 'password' })
      return
    }
    router.replace(route.query.redirect || '/overview')
  } catch {
    // 错误提示由拦截器统一处理
  } finally {
    loading.value = false
  }
}
</script>
