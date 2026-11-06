<template>
  <el-container style="height: 100vh">
    <el-aside width="210px" class="cz-sidebar">
      <div class="cz-logo">
        <span class="cz-logo-dot"></span>
        <span>CZAPI 网关</span>
      </div>
      <el-menu
        :default-active="$route.path"
        background-color="#121a22"
        text-color="#d8e0e8"
        active-text-color="#ffffff"
        router
      >
        <el-menu-item index="/overview">
          <el-icon><DataLine /></el-icon><span>概览</span>
        </el-menu-item>
        <el-menu-item index="/sites">
          <el-icon><Connection /></el-icon><span>站点管理</span>
        </el-menu-item>
        <el-menu-item index="/accounts">
          <el-icon><User /></el-icon><span>账户与额度</span>
        </el-menu-item>
        <el-menu-item index="/ledger">
          <el-icon><Tickets /></el-icon><span>账本对账</span>
        </el-menu-item>
        <el-menu-item index="/keys">
          <el-icon><Key /></el-icon><span>Key 管理</span>
        </el-menu-item>
        <el-menu-item index="/hosts">
          <el-icon><Monitor /></el-icon><span>Host 与订阅</span>
        </el-menu-item>
        <el-menu-item index="/usage">
          <el-icon><TrendCharts /></el-icon><span>用量报表</span>
        </el-menu-item>
      </el-menu>
    </el-aside>

    <el-container>
      <el-header class="cz-header">
        <span class="cz-header-title">{{ $route.meta.title || '' }}</span>
        <el-dropdown @command="onCommand">
          <span style="cursor: pointer; display: flex; align-items: center; gap: 6px">
            <el-icon><UserFilled /></el-icon>
            {{ me.username || '—' }}
            <el-icon><ArrowDown /></el-icon>
          </span>
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item command="password">修改密码</el-dropdown-item>
              <el-dropdown-item command="logout" divided>退出登录</el-dropdown-item>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
      </el-header>

      <el-main style="padding: 0; background: #f5f7fa">
        <router-view />
      </el-main>
    </el-container>
  </el-container>
</template>

<script setup>
import { onMounted, reactive } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import http from '../api'

const router = useRouter()
const me = reactive({ username: '', version: '' })

onMounted(async () => {
  try {
    const data = await http.get('/me')
    me.username = data.username
    me.version = data.version
  } catch {
    // 拦截器会处理跳转
  }
})

async function onCommand(cmd) {
  if (cmd === 'password') {
    router.push({ name: 'password' })
    return
  }
  if (cmd === 'logout') {
    await http.post('/logout')
    ElMessage.success('已退出')
    router.replace({ name: 'login' })
  }
}
</script>
