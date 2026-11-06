<template>
  <div class="cz-page" style="max-width: 520px">
    <el-alert
      v-if="forced"
      type="warning"
      :closable="false"
      show-icon
      title="请先修改默认密码"
      description="当前仍在使用初始密码，修改后才能进入后台其它页面。"
      style="margin-bottom: 16px"
    />

    <el-card shadow="never">
      <el-form label-width="100px">
        <el-form-item label="原密码">
          <el-input v-model="oldPassword" type="password" show-password />
        </el-form-item>
        <el-form-item label="新密码">
          <el-input v-model="newPassword" type="password" show-password />
          <div class="cz-hint">至少 8 位，且不能与用户名相同。</div>
        </el-form-item>
        <el-form-item label="确认新密码">
          <el-input v-model="confirmPassword" type="password" show-password />
        </el-form-item>
        <el-form-item>
          <el-button type="primary" :loading="saving" @click="submit">保存</el-button>
          <el-button v-if="!forced" @click="$router.back()">返回</el-button>
        </el-form-item>
      </el-form>
    </el-card>
  </div>
</template>

<script setup>
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import http from '../api'

const router = useRouter()
const oldPassword = ref('')
const newPassword = ref('')
const confirmPassword = ref('')
const saving = ref(false)
const forced = ref(false)

onMounted(async () => {
  try {
    const me = await http.get('/me')
    forced.value = !!me.must_change_password
  } catch {
    // 拦截器处理
  }
})

async function submit() {
  if (!oldPassword.value || !newPassword.value) {
    ElMessage.warning('请填写完整')
    return
  }
  if (newPassword.value !== confirmPassword.value) {
    ElMessage.warning('两次输入的新密码不一致')
    return
  }
  if (newPassword.value.length < 8) {
    ElMessage.warning('新密码至少 8 位')
    return
  }

  saving.value = true
  try {
    await http.post('/password', {
      old_password: oldPassword.value,
      new_password: newPassword.value,
    })
    ElMessage.success('密码已修改')
    router.replace('/overview')
  } catch {
    // 拦截器处理
  } finally {
    saving.value = false
  }
}
</script>
