<template>
  <RouterView v-slot="{ Component, route }"><component :is="Component" :key="route.path.startsWith('/login') || route.path.startsWith('/register') ? route.path : undefined" /></RouterView>
</template>
