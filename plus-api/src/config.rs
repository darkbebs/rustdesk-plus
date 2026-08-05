use rand::Rng;
use serde::{Deserialize, Serialize};
use sqlx::PgPool;
use std::path::Path;
use uuid::Uuid;

/// Configuração global do servidor (compartilhada por todos os tenants).
/// rustdesk_password saiu daqui e foi para tenant_config.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct ServerConfig {
    pub server_ip: String,
    pub server_key: String,
    pub api_url: String,
}

/// Codigo de instalacao: vai na URL (/i/CODIGO) e e digitado a mao, entao
/// continua so com maiusculas e digitos. Nao segue as regras de senha do
/// RustDesk porque nao e uma senha.
fn generate_install_code() -> String {
    const CHARSET: &[u8] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789";
    let mut rng = rand::thread_rng();
    (0..8).map(|_| CHARSET[rng.gen_range(0..CHARSET.len())] as char).collect()
}

/// Gera senha aceita pelo cliente RustDesk.
///
/// O cliente exige digito, maiuscula, minuscula e tamanho >= 8. A versao antiga
/// usava so maiusculas e digitos: o `--password` recusava a senha em silencio
/// (saindo com codigo 0) e a maquina ficava com senha de uso unico.
///
/// Sem caracteres ambiguos (O/0, I/l/1) porque a senha e digitada a mao.
fn generate_password() -> String {
    const UPPER: &[u8] = b"ABCDEFGHJKLMNPQRSTUVWXYZ";
    const LOWER: &[u8] = b"abcdefghijkmnpqrstuvwxyz";
    const DIGIT: &[u8] = b"23456789";

    let mut rng = rand::thread_rng();
    let pick = |rng: &mut rand::rngs::ThreadRng, set: &[u8]| set[rng.gen_range(0..set.len())] as char;

    // Uma de cada classe garante as regras; o resto completa o tamanho.
    let mut chars = vec![
        pick(&mut rng, UPPER),
        pick(&mut rng, LOWER),
        pick(&mut rng, DIGIT),
    ];
    let all: Vec<u8> = [UPPER, LOWER, DIGIT].concat();
    while chars.len() < 10 {
        chars.push(pick(&mut rng, &all));
    }
    // Embaralha para as tres primeiras posicoes nao serem sempre da mesma classe.
    for i in (1..chars.len()).rev() {
        chars.swap(i, rng.gen_range(0..=i));
    }
    chars.into_iter().collect()
}

/// Regras que o cliente RustDesk aplica ao definir senha permanente.
///
/// Ele recusa em silencio: o `--password` sai com codigo 0 e nao grava nada, e a
/// maquina fica com senha de uso unico. Melhor barrar na origem.
pub fn validate_rustdesk_password(pwd: &str) -> Result<(), crate::error::AppError> {
    let mut faltando = Vec::new();
    if pwd.chars().count() < 8 {
        faltando.push("pelo menos 8 caracteres");
    }
    if !pwd.chars().any(|c| c.is_ascii_digit()) {
        faltando.push("um dígito");
    }
    if !pwd.chars().any(|c| c.is_ascii_uppercase()) {
        faltando.push("uma letra maiúscula");
    }
    if !pwd.chars().any(|c| c.is_ascii_lowercase()) {
        faltando.push("uma letra minúscula");
    }
    if faltando.is_empty() {
        return Ok(());
    }
    Err(crate::error::AppError::BadRequest(format!(
        "O RustDesk exige senha com {}.",
        faltando.join(", ")
    )))
}

async fn upsert_global(db: &PgPool, key: &str, value: &str) -> anyhow::Result<()> {
    sqlx::query(
        "INSERT INTO server_config (key, value, updated_at) VALUES ($1, $2, now()) \
         ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()",
    )
    .bind(key)
    .bind(value)
    .execute(db)
    .await?;
    Ok(())
}

// ── Senha por tenant ──────────────────────────────────────────────────────────

async fn upsert_tenant(db: &PgPool, tenant_id: Uuid, key: &str, value: &str) -> anyhow::Result<()> {
    sqlx::query(
        "INSERT INTO tenant_config (tenant_id, key, value, updated_at) VALUES ($1, $2, $3, now()) \
         ON CONFLICT (tenant_id, key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()",
    )
    .bind(tenant_id)
    .bind(key)
    .bind(value)
    .execute(db)
    .await?;
    Ok(())
}

pub async fn load_tenant_password(db: &PgPool, tenant_id: Uuid) -> anyhow::Result<String> {
    let pwd: Option<String> = sqlx::query_scalar(
        "SELECT value FROM tenant_config WHERE tenant_id = $1 AND key = 'rustdesk_password'",
    )
    .bind(tenant_id)
    .fetch_optional(db)
    .await?;
    Ok(pwd.unwrap_or_default())
}

pub async fn save_tenant_password(db: &PgPool, tenant_id: Uuid, password: &str) -> anyhow::Result<()> {
    upsert_tenant(db, tenant_id, "rustdesk_password", password).await?;
    invalidate_tenant_installer(tenant_id).await;
    Ok(())
}

// ── Interruptor global do agente de gerenciamento (variável de ambiente) ───────

/// Liga/desliga TODA a funcionalidade de agente (geração, instalação, terminal,
/// scripts e conexão WS). Controlado pela env `AGENT_ENABLED`. Padrão: desligado.
pub fn agent_enabled() -> bool {
    std::env::var("AGENT_ENABLED")
        .map(|v| matches!(v.trim().to_ascii_lowercase().as_str(), "1" | "true" | "yes" | "on"))
        .unwrap_or(false)
}

pub async fn load_tenant_install_code(db: &PgPool, tenant_id: Uuid) -> anyhow::Result<String> {
    let code: Option<String> = sqlx::query_scalar(
        "SELECT value FROM tenant_config WHERE tenant_id = $1 AND key = 'install_code'",
    )
    .bind(tenant_id)
    .fetch_optional(db)
    .await?;
    Ok(code.unwrap_or_default())
}

/// Garante que o tenant tem um código de instalação; retorna o código.
pub async fn ensure_tenant_install_code(db: &PgPool, tenant_id: Uuid) -> anyhow::Result<String> {
    let existing = load_tenant_install_code(db, tenant_id).await?;
    if !existing.is_empty() {
        return Ok(existing);
    }
    let code = generate_install_code();
    upsert_tenant(db, tenant_id, "install_code", &code).await?;
    Ok(code)
}

/// Garante que o tenant tem uma senha gerada; retorna a senha (nova ou existente).
pub async fn ensure_tenant_password(db: &PgPool, tenant_id: Uuid) -> anyhow::Result<String> {
    let existing = load_tenant_password(db, tenant_id).await?;
    if !existing.is_empty() {
        return Ok(existing);
    }
    let pwd = generate_password();
    upsert_tenant(db, tenant_id, "rustdesk_password", &pwd).await?;
    Ok(pwd)
}

// ── Config global ─────────────────────────────────────────────────────────────

pub async fn synchronize(db: &PgPool) -> anyhow::Result<()> {
    let key_path = std::env::var("RUSTDESK_KEY_PATH")
        .unwrap_or_else(|_| "/rustdesk-data/id_ed25519.pub".to_string());
    if let Ok(key) = tokio::fs::read_to_string(key_path).await {
        let key = key.trim();
        if !key.is_empty() {
            upsert_global(db, "server_key", key).await?;
        }
    }

    for (env_key, config_key) in [
        ("PUBLIC_HOST", "server_ip"),
        ("PUBLIC_API_URL", "api_url"),
    ] {
        if let Ok(value) = std::env::var(env_key) {
            if !value.trim().is_empty() {
                let exists: bool = sqlx::query_scalar(
                    "SELECT EXISTS(SELECT 1 FROM server_config WHERE key = $1 AND value <> '')",
                )
                .bind(config_key)
                .fetch_one(db)
                .await?;
                if !exists {
                    upsert_global(db, config_key, value.trim()).await?;
                }
            }
        }
    }

    if let Some(host) =
        sqlx::query_scalar::<_, String>("SELECT value FROM server_config WHERE key = 'server_ip'")
            .fetch_optional(db)
            .await?
    {
        write_public_host(host.trim()).await?;
    }
    Ok(())
}

pub async fn load(db: &PgPool) -> anyhow::Result<ServerConfig> {
    synchronize(db).await?;
    let rows: Vec<(String, String)> =
        sqlx::query_as("SELECT key, value FROM server_config").fetch_all(db).await?;
    let mut config = ServerConfig {
        server_ip: String::new(),
        server_key: String::new(),
        api_url: String::new(),
    };
    for (key, value) in rows {
        match key.as_str() {
            "server_ip" => config.server_ip = value,
            "server_key" => config.server_key = value,
            "api_url" => config.api_url = value,
            _ => {}
        }
    }
    Ok(config)
}

pub async fn save(db: &PgPool, config: &ServerConfig) -> anyhow::Result<()> {
    upsert_global(db, "server_ip", config.server_ip.trim()).await?;
    upsert_global(db, "server_key", config.server_key.trim()).await?;
    upsert_global(db, "api_url", config.api_url.trim_end_matches('/')).await?;
    write_public_host(config.server_ip.trim()).await?;
    Ok(())
}

pub async fn write_public_host(host: &str) -> anyhow::Result<()> {
    if host.is_empty() {
        return Ok(());
    }
    let path = std::env::var("DEPLOYMENT_HOST_PATH")
        .unwrap_or_else(|_| "/deployment/public_host".to_string());
    if let Some(parent) = Path::new(&path).parent() {
        tokio::fs::create_dir_all(parent).await?;
    }
    tokio::fs::write(path, host.as_bytes()).await?;
    Ok(())
}

pub async fn invalidate_tenant_installer(tenant_id: Uuid) {
    let base = std::env::var("INSTALLER_PATH")
        .unwrap_or_else(|_| "/app/generated/rustdesk-installer.exe".to_string());
    let dir = std::path::Path::new(&base)
        .parent()
        .unwrap_or_else(|| std::path::Path::new("/app/generated"));
    let _ = tokio::fs::remove_file(dir.join(format!("installer-{tenant_id}.exe"))).await;
    let _ = tokio::fs::remove_file(dir.join(format!("installer-config-{tenant_id}.json"))).await;
}
