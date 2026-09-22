-- Match text-embedding-v3 and the Prisma schema. Existing incompatible
-- vectors cause this migration to fail rather than silently losing data.
ALTER TABLE "knowledge_chunk"
  ALTER COLUMN "embedding" TYPE vector(1024);
